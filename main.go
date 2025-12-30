package main

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

/* ==========================================
   CONFIG & UPDATE SYSTEM
========================================== */
const AppVersion = "1.2" // Versi dengan Full TUI Support
const ConfigURL = "https://raw.githubusercontent.com/tangsanrich/Fileku/main/executor.txt"
const CryptoKey = "RahasiaNegaraJanganSampaiBocorir"

// MAX Scrollback
const MaxScrollback = 100 // Dikurangi agar TUI lebih responsif

var currentDir string = "/sdcard" 
var activeStdin *os.File
var cmdMutex sync.Mutex

type OnlineConfig struct {
	Version string `json:"version"`
	Message string `json:"message"`
	Link    string `json:"link"`
}

//go:embed fd.png
var fdPng []byte

//go:embed bg.png
var bgPng []byte

//go:embed driver.zip
var driverZip []byte

/* ==========================================
   SECURITY LOGIC
========================================== */
func decryptConfig(encryptedStr string) ([]byte, error) {
	defer func() { if r := recover(); r != nil {} }()
	key := []byte(CryptoKey)
	if len(key) != 32 { return nil, errors.New("key length error") }
	encryptedStr = strings.TrimSpace(encryptedStr)
	data, err := base64.StdEncoding.DecodeString(encryptedStr)
	if err != nil { return nil, err }
	block, err := aes.NewCipher(key)
	if err != nil { return nil, err }
	gcm, err := cipher.NewGCM(block)
	if err != nil { return nil, err }
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize { return nil, errors.New("data corrupt") }
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil { return nil, err }
	return plaintext, nil
}

/* ==========================================
   ADVANCED TERMINAL EMULATOR (VT100 COMPATIBLE)
========================================== */
type Terminal struct {
	grid         *widget.TextGrid
	scroll       *container.Scroll
	curRow       int
	curCol       int
	curStyle     *widget.CustomTextGridStyle
	mutex        sync.Mutex
	needsRefresh bool
	escBuffer    []byte
	inEsc        bool
}

func NewTerminal() *Terminal {
	g := widget.NewTextGrid()
	g.ShowLineNumbers = false
	// PENTING: Gunakan font monospace default sistem
	g.SetStyleRange(0, 0, 0, 0, widget.TextGridStyleDefault)

	defStyle := &widget.CustomTextGridStyle{
		FGColor: theme.ForegroundColor(),
		BGColor: color.Transparent,
	}

	term := &Terminal{
		grid:         g,
		scroll:       container.NewScroll(g),
		curRow:       0,
		curCol:       0,
		curStyle:     defStyle,
		inEsc:        false,
		escBuffer:    make([]byte, 0, 100),
		needsRefresh: false,
	}

	// Inisialisasi grid kosong agar cursor move tidak panic
	term.ensureSize(40, 100)

	// Refresher loop yang lebih cepat (60fps target)
	go func() {
		ticker := time.NewTicker(16 * time.Millisecond)
		for range ticker.C {
			term.mutex.Lock()
			if term.needsRefresh {
				term.grid.Refresh()
				// Untuk TUI statis (seperti mod menu), scroll to bottom kadang mengganggu
				// Tapi kita biarkan untuk command line biasa
				// term.scroll.ScrollToBottom() 
				term.needsRefresh = false
			}
			term.mutex.Unlock()
		}
	}()
	return term
}

func (t *Terminal) Clear() {
	t.mutex.Lock()
	t.grid.SetText("")
	t.ensureSize(40, 100) // Reset size
	t.curRow = 0
	t.curCol = 0
	t.curStyle.FGColor = theme.ForegroundColor()
	t.curStyle.BGColor = color.Transparent
	t.needsRefresh = true
	t.mutex.Unlock()
}

func (t *Terminal) Write(p []byte) (int, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	for _, b := range p {
		if b == 0x1b { // ESC Start
			t.inEsc = true
			t.escBuffer = t.escBuffer[:0]
			continue
		}

		if t.inEsc {
			t.escBuffer = append(t.escBuffer, b)
			// CSI sequences biasanya berakhir huruf, atau simbol tertentu
			if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '@' || b == '`' {
				t.handleCSI(t.escBuffer)
				t.inEsc = false
			}
			if len(t.escBuffer) > 50 { t.inEsc = false } // Safety break
			continue
		}

		// Handle Standard Control Characters
		switch b {
		case '\n': // Line Feed
			t.curRow++
			// Di Raw Mode/TUI, \n kadang tidak mereset Col, tapi biasanya iya.
			// Kita reset col jika tidak dalam TUI complex, tapi kebanyakan TUI pakai \r\n
		case '\r': // Carriage Return
			t.curCol = 0
		case '\b', 0x7f: // Backspace
			if t.curCol > 0 { t.curCol-- }
		case '\t': // Tab
			t.curCol += 4
		case 0x07: // Bell (ignore)
		default:
			// Print Printable Character
			t.setChar(rune(b))
			t.curCol++
		}
	}
	t.needsRefresh = true
	return len(p), nil
}

// Logic Parsing ANSI yang ditingkatkan untuk Positioning
func (t *Terminal) handleCSI(seq []byte) {
	if len(seq) < 1 || seq[0] != '[' { return }
	
	cmd := seq[len(seq)-1]
	paramsStr := string(seq[1 : len(seq)-1])
	
	// Split parameters: "31;1" -> [31, 1]
	var params []int
	if len(paramsStr) > 0 {
		// Handle cases like "?25l" (Private modes) - ignore leading '?'
		cleanParams := strings.TrimPrefix(paramsStr, "?")
		rawParts := strings.Split(cleanParams, ";")
		for _, p := range rawParts {
			var val int
			if _, err := fmt.Sscanf(p, "%d", &val); err == nil {
				params = append(params, val)
			} else {
				params = append(params, 0)
			}
		}
	} else {
		params = []int{0} // Default param usually 0 or 1
	}

	// Helper to get param with default
	getParam := func(idx, def int) int {
		if idx < len(params) { 
			val := params[idx]
			if val == 0 { return def } // 0 usually means default in standard
			return val 
		}
		return def
	}

	switch cmd {
	case 'm': // SGR - Warna & Style
		t.handleSGR(params)

	// --- CURSOR MOVEMENT (Kunci agar TUI Rapi) ---
	case 'H', 'f': // Cursor Position [row;colH
		// ANSI index start from 1, TextGrid start from 0
		row := getParam(0, 1) - 1
		col := getParam(1, 1) - 1
		t.curRow = row
		t.curCol = col
		t.ensureSize(t.curRow+1, t.curCol+1)

	case 'A': // Cursor Up
		t.curRow -= getParam(0, 1)
		if t.curRow < 0 { t.curRow = 0 }
	case 'B': // Cursor Down
		t.curRow += getParam(0, 1)
		t.ensureSize(t.curRow+1, t.curCol)
	case 'C': // Cursor Forward
		t.curCol += getParam(0, 1)
	case 'D': // Cursor Back
		t.curCol -= getParam(0, 1)
		if t.curCol < 0 { t.curCol = 0 }
	case 'G': // Cursor Horizontal Absolute
		t.curCol = getParam(0, 1) - 1
		if t.curCol < 0 { t.curCol = 0 }
	case 'd': // Vertical Line Absolute
		t.curRow = getParam(0, 1) - 1
		t.ensureSize(t.curRow+1, t.curCol)

	// --- ERASING ---
	case 'J': // Erase in Display
		mode := getParam(0, 0)
		if mode == 2 { // Clear Entire Screen
			// Kita tidak menghapus history (textgrid rows) agar performa tetap baik,
			// tapi kita isi layar visible dengan spasi kosong.
			// Untuk simpelnya di Fyne: Reset text jika clear all
			t.grid.SetText("")
			t.ensureSize(40, 100) // Restore buffer size
			t.curRow = 0
			t.curCol = 0
		}
	case 'K': // Erase in Line
		mode := getParam(0, 0)
		t.ensureSize(t.curRow+1, t.curCol+1)
		rowCells := t.grid.Rows[t.curRow].Cells
		if mode == 0 { // Clear cursor to end
			if t.curCol < len(rowCells) {
				t.grid.SetRow(t.curRow, widget.TextGridRow{Cells: rowCells[:t.curCol]})
			}
		}
	
	// --- MODES (Ignore for now) ---
	case 'l', 'h': 
		// Hide/Show cursor (?25l), Alternate Screen buffer (?1049h)
		// Mengabaikan ini tidak akan membuat crash, hanya cursor tetap muncul.
	}
}

func (t *Terminal) handleSGR(params []int) {
	i := 0
	for i < len(params) {
		code := params[i]
		switch code {
		case 0: // Reset
			t.curStyle.FGColor = theme.ForegroundColor()
			t.curStyle.BGColor = color.Transparent
			t.curStyle.TextStyle = fyne.TextStyle{}
		case 1: t.curStyle.TextStyle.Bold = true
		// Colors
		case 30, 31, 32, 33, 34, 35, 36, 37, 90, 91, 92, 93, 94, 95, 96, 97:
			t.curStyle.FGColor = ansiToSimpleColor(code)
		case 39: t.curStyle.FGColor = theme.ForegroundColor()
		case 40, 41, 42, 43, 44, 45, 46, 47:
			t.curStyle.BGColor = ansiToSimpleColor(code - 10)
		case 49: t.curStyle.BGColor = color.Transparent
		// RGB support
		case 38:
			if i+4 < len(params) && params[i+1] == 2 {
				t.curStyle.FGColor = color.RGBA{R: uint8(params[i+2]), G: uint8(params[i+3]), B: uint8(params[i+4]), A: 255}
				i += 4
			} else if i+2 < len(params) && params[i+1] == 5 {
				t.curStyle.FGColor = ansi256ToColor(params[i+2])
				i += 2
			}
		}
		i++
	}
}

// Memastikan grid memiliki ukuran minimal baris & kolom
func (t *Terminal) ensureSize(rows, cols int) {
	// 1. Expand Rows
	for len(t.grid.Rows) <= rows {
		t.grid.SetRow(len(t.grid.Rows), widget.TextGridRow{Cells: []widget.TextGridCell{}})
	}
	
	// Note: Kita tidak perlu memaksa columns (padding) di semua baris 
	// karena Fyne TextGrid menangani variable length row.
	// Padding hanya dilakukan saat setChar.
}

func (t *Terminal) setChar(r rune) {
	t.ensureSize(t.curRow, t.curCol)
	
	rowCells := t.grid.Rows[t.curRow].Cells
	
	// Padding spasi jika kursor lompat jauh ke kanan (akibat CSI G/CSI H)
	if t.curCol >= len(rowCells) {
		newCells := make([]widget.TextGridCell, t.curCol+1)
		copy(newCells, rowCells)
		// Isi gap dengan spasi kosong style default
		for i := len(rowCells); i < t.curCol; i++ {
			newCells[i] = widget.TextGridCell{Rune: ' '}
		}
		rowCells = newCells
	}
	
	styleCopy := *t.curStyle
	
	// Overwrite karakter jika posisi sudah ada (PENTING BUAT MENU)
	if t.curCol < len(rowCells) {
		rowCells[t.curCol] = widget.TextGridCell{Rune: r, Style: &styleCopy}
	} else {
		// Harusnya terhandle padding diatas, tapi double check
		rowCells = append(rowCells, widget.TextGridCell{Rune: r, Style: &styleCopy})
	}

	t.grid.SetRow(t.curRow, widget.TextGridRow{Cells: rowCells})
}

func ansiToSimpleColor(code int) color.Color {
	switch code {
	case 30, 90: return color.Gray{Y: 100}
	case 31, 91: return theme.ErrorColor()
	case 32, 92: return theme.SuccessColor()
	case 33, 93: return theme.WarningColor()
	case 34, 94: return theme.PrimaryColor()
	case 35, 95: return color.RGBA{R: 200, G: 0, B: 200, A: 255}
	case 36, 96: return color.RGBA{R: 0, G: 255, B: 255, A: 255}
	case 37, 97: return theme.ForegroundColor()
	default: return theme.ForegroundColor()
	}
}

func ansi256ToColor(idx int) color.Color {
	if idx < 16 { return ansiToSimpleColor(30 + idx) }
	if idx == 232 { return color.Black }
	if idx == 255 { return color.White }
	return theme.PrimaryColor()
}

/* ===============================
   SYSTEM HELPERS (SAME AS BEFORE)
================================ */
func CheckRoot() bool {
	cmd := exec.Command("su", "-c", "id -u")
	out, err := cmd.Output()
	if err != nil { return false }
	return strings.TrimSpace(string(out)) == "0"
}

func CheckKernelDriver() bool {
	signature := "read_physical_address"
	cmd := exec.Command("su", "-c", fmt.Sprintf("grep -q '%s' /proc/kallsyms", signature))
	return cmd.Run() == nil
}

func CheckSELinux() string {
	cmd := exec.Command("su", "-c", "getenforce")
	out, err := cmd.Output()
	if err != nil { return "Unknown" }
	return strings.TrimSpace(string(out))
}

func createLabel(text string, color color.Color, size float32, bold bool) *canvas.Text {
	lbl := canvas.NewText(text, color)
	lbl.TextSize = size; lbl.Alignment = fyne.TextAlignCenter
	if bold { lbl.TextStyle = fyne.TextStyle{Bold: true} }
	return lbl
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil { return err }
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil { return err }
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

/* ===============================
              MAIN UI
================================ */
func main() {
	a := app.New()
	a.Settings().SetTheme(theme.DarkTheme())

	w := a.NewWindow("TANGSAN EXECUTOR (TUI)")
	w.Resize(fyne.NewSize(400, 700))
	w.SetMaster()

	term := NewTerminal()
	
	go func() {
		time.Sleep(1 * time.Second)
		if !CheckRoot() { /* Auto Check */ }
	}()

	if !CheckRoot() { currentDir = "/sdcard" }

	brightYellow := color.RGBA{R: 255, G: 255, B: 0, A: 255}
	successGreen := color.RGBA{R: 0, G: 255, B: 0, A: 255}
	failRed := color.RGBA{R: 255, G: 50, B: 50, A: 255}
	silverColor := color.Gray{Y: 180}

	input := widget.NewEntry()
	input.SetPlaceHolder("Terminal Command...")
	status := canvas.NewText("System: Ready", silverColor)
	status.TextSize = 12; status.Alignment = fyne.TextAlignCenter

	lblKernelTitle := createLabel("KERNEL", brightYellow, 10, true)
	lblKernelValue := createLabel("...", color.Gray{Y: 150}, 11, true)
	lblSELinuxTitle := createLabel("SELINUX", brightYellow, 10, true)
	lblSELinuxValue := createLabel("...", color.Gray{Y: 150}, 11, true)
	lblSystemTitle := createLabel("ROOT", brightYellow, 10, true)
	lblSystemValue := createLabel("...", color.Gray{Y: 150}, 11, true)

	go func() {
		time.Sleep(1 * time.Second)
		for {
			func() {
				defer func() { if r := recover(); r != nil {} }()
				if CheckRoot() {
					lblSystemValue.Text = "GRANTED"; lblSystemValue.Color = successGreen
				} else {
					lblSystemValue.Text = "DENIED"; lblSystemValue.Color = failRed
				}
				lblSystemValue.Refresh()
				
				if CheckKernelDriver() {
					lblKernelValue.Text = "ACTIVE"; lblKernelValue.Color = successGreen
				} else {
					lblKernelValue.Text = "MISSING"; lblKernelValue.Color = failRed
				}
				lblKernelValue.Refresh()
				
				se := CheckSELinux()
				lblSELinuxValue.Text = strings.ToUpper(se)
				if se == "Enforcing" { lblSELinuxValue.Color = successGreen } else if se == "Permissive" { lblSELinuxValue.Color = failRed } else { lblSELinuxValue.Color = color.Gray{Y: 150} }
				lblSELinuxValue.Refresh()
			}()
			time.Sleep(3 * time.Second)
		}
	}()

	// -------------------------------------------------------------
	// EXECUTION LOGIC DENGAN PTY + ENV TUI
	// -------------------------------------------------------------
	executeTask := func(cmdText string, isScript bool, scriptPath string, isBinary bool) {
		status.Text = "Status: Running TUI..."
		status.Refresh()

		// Jika bukan TUI app, kita print prompt
		if !isScript {
			// term.Write(...) // Opsional: Tampilkan prompt
		}

		go func() {
			var cmd *exec.Cmd
			isRoot := CheckRoot()

			if isScript {
				if isRoot {
					target := "/data/local/tmp/temp_exec"
					exec.Command("su", "-c", "rm -f "+target).Run()
					exec.Command("su", "-c", fmt.Sprintf("cp %s %s && chmod 777 %s", scriptPath, target, target)).Run()
					if !isBinary { cmd = exec.Command("su", "-c", "sh "+target) } else { cmd = exec.Command("su", "-c", target) }
				} else {
					if !isBinary { cmd = exec.Command("sh", scriptPath) } else { cmd = exec.Command(scriptPath) }
				}
			} else {
				if isRoot {
					cmd = exec.Command("su", "-c", fmt.Sprintf("cd \"%s\" && %s", currentDir, cmdText))
				} else {
					runCmd := cmdText
					if strings.HasPrefix(cmdText, "ls") { if !strings.Contains(cmdText, "-a") { runCmd = strings.Replace(cmdText, "ls", "ls -a", 1) } }
					if strings.HasPrefix(cmdText, "./") {
						fileName := strings.TrimPrefix(cmdText, "./")
						fullPath := filepath.Join(currentDir, fileName)
						if strings.HasSuffix(fileName, ".sh") {
							runCmd = fmt.Sprintf("sh \"%s\"", fullPath)
						} else {
							tmpBin := filepath.Join(os.TempDir(), fileName)
							if err := copyFile(fullPath, tmpBin); err == nil { os.Chmod(tmpBin, 0755); runCmd = tmpBin } else { runCmd = fullPath }
						}
					}
					cmd = exec.Command("sh", "-c", runCmd)
					cmd.Dir = currentDir
				}
			}

			// 1. Force Colors & UTF-8 for Box Drawing
			cmd.Env = append(os.Environ(), 
				"TERM=xterm-256color",
				"COLORTERM=truecolor",
				"LANG=en_US.UTF-8", // Penting agar garis kotak TUI muncul
				"LC_ALL=en_US.UTF-8",
			)

			// 2. Start PTY
			ptmx, err := pty.Start(cmd)
			if err != nil {
				term.Write([]byte(fmt.Sprintf("\x1b[31m[PTY ERR] %s\x1b[0m\n", err.Error())))
				status.Text = "Error"; status.Refresh()
				return
			}

			// 3. SET UKURAN TERMINAL (CRUCIAL UNTUK TUI)
			// Ukuran 40 baris x 100 kolom biasanya cukup untuk mod menu landscape
			pty.Setsize(ptmx, &pty.Winsize{Rows: 40, Cols: 100, X: 0, Y: 0})

			cmdMutex.Lock()
			activeStdin = ptmx
			cmdMutex.Unlock()

			defer func() {
				_ = ptmx.Close()
				cmdMutex.Lock()
				activeStdin = nil
				cmdMutex.Unlock()
				status.Text = "Status: Idle"; status.Refresh()
				if isScript && isRoot { exec.Command("su", "-c", "rm -f /data/local/tmp/temp_exec").Run() }
			}()

			io.Copy(term, ptmx)
			cmd.Wait()
		}()
	}

	send := func() {
		text := input.Text
		input.SetText("")
		
		cmdMutex.Lock()
		if activeStdin != nil {
			// Kirim input user ke PTY
			activeStdin.Write([]byte(text + "\n")) 
			cmdMutex.Unlock()
			return 
		}
		cmdMutex.Unlock()
		
		if strings.TrimSpace(text) == "" { return }
		
		if strings.HasPrefix(text, "cd") {
			parts := strings.Fields(text)
			newPath := currentDir
			if len(parts) == 1 { if CheckRoot() { newPath = "/data/local/tmp" } else { h, _ := os.UserHomeDir(); newPath = h } } else { arg := parts[1]; if filepath.IsAbs(arg) { newPath = arg } else { newPath = filepath.Join(currentDir, arg) } }
			newPath = filepath.Clean(newPath)
			exist := false
			if CheckRoot() { if exec.Command("su", "-c", "[ -d \""+newPath+"\" ]").Run() == nil { exist = true } } else { if info, err := os.Stat(newPath); err == nil && info.IsDir() { exist = true } }
			if exist { currentDir = newPath; term.Write([]byte(fmt.Sprintf("cd %s\n", parts[1]))) } else { term.Write([]byte(fmt.Sprintf("cd fail\n"))) }
			return
		}
		executeTask(text, false, "", false)
	}
	input.OnSubmitted = func(_ string) { send() }

	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close(); term.Clear()
		data, err := io.ReadAll(reader)
		if err != nil { term.Write([]byte("\x1b[31m[ERR] Read Failed\x1b[0m\n")); return }
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))
		tmpFile, err := os.CreateTemp("", "exec_tmp")
		if err != nil { term.Write([]byte("\x1b[31m[ERR] Write Failed\x1b[0m\n")); return }
		tmpPath := tmpFile.Name(); tmpFile.Write(data); tmpFile.Close(); os.Chmod(tmpPath, 0755)
		executeTask("", true, tmpPath, isBinary)
	}

	var overlayContainer *fyne.Container
	
	showModal := func(title, msg, confirm string, action func(), isErr bool, isForce bool) {
		w.Canvas().Refresh(w.Content())
		cancelLabel := "BATAL"
		cancelFunc := func() { overlayContainer.Hide() }
		if isForce { cancelLabel = "KELUAR"; cancelFunc = func() { os.Exit(0) } }
		
		btnCancel := widget.NewButton(cancelLabel, cancelFunc)
		btnCancel.Importance = widget.DangerImportance
		
		btnOk := widget.NewButton(confirm, func() {
			if !isForce { overlayContainer.Hide() }
			if action != nil { action() }
		})
		
		if confirm == "COBA LAGI" { btnOk.Importance = widget.HighImportance } else {
			if isErr { btnOk.Importance = widget.DangerImportance } else { btnOk.Importance = widget.HighImportance }
		}
		
		btnBox := container.NewHBox(layout.NewSpacer(), container.NewGridWrap(fyne.NewSize(110,40), btnCancel), widget.NewLabel("   "), container.NewGridWrap(fyne.NewSize(110,40), btnOk), layout.NewSpacer())
		lblTitle := createLabel(title, theme.ForegroundColor(), 18, true)
		if isErr { lblTitle.Color = theme.ErrorColor() }
		lblMsg := widget.NewLabel(msg)
		lblMsg.Alignment = fyne.TextAlignCenter; lblMsg.Wrapping = fyne.TextWrapWord
		content := container.NewVBox(container.NewPadded(container.NewCenter(lblTitle)), container.NewPadded(lblMsg), widget.NewLabel(""), btnBox)
		card := widget.NewCard("", "", container.NewPadded(content))
		wrapper := container.NewCenter(container.NewGridWrap(fyne.NewSize(300, 220), container.NewPadded(card)))
		overlayContainer.Objects = []fyne.CanvasObject{canvas.NewRectangle(color.RGBA{0,0,0,220}), wrapper}
		overlayContainer.Show(); overlayContainer.Refresh()
	}

	autoInstallKernel := func() {
		term.Clear()
		status.Text = "Sistem: Memproses..."
		status.Refresh()
		go func() {
			term.Write([]byte("\x1b[36m╔════ PENGINSTAL DRIVER ════╗\x1b[0m\n"))
			out, _ := exec.Command("uname", "-r").Output()
			fullVer := strings.TrimSpace(string(out))
			targetVer := strings.Split(fullVer, "-")[0]
			term.Write([]byte(fmt.Sprintf("Kernel: \x1b[33m%s\x1b[0m\n", fullVer)))

			targetKoPath := "/data/local/tmp/module_inject.ko"
			defer func() { exec.Command("su", "-c", "rm -f "+targetKoPath).Run() }()

			term.Write([]byte("\x1b[97m[*] Membaca file driver internal...\x1b[0m\n"))
			zipReader, err := zip.NewReader(bytes.NewReader(driverZip), int64(len(driverZip)))
			if err != nil { term.Write([]byte("\x1b[31m[ERR] File Zip Rusak/Corrupt\x1b[0m\n")); return }

			var fileToExtract *zip.File
			for _, f := range zipReader.File { if strings.HasSuffix(f.Name, ".ko") && strings.Contains(f.Name, targetVer) { fileToExtract = f; break } }
			if fileToExtract == nil { for _, f := range zipReader.File { if strings.HasSuffix(f.Name, ".ko") { fileToExtract = f; break } } }

			if fileToExtract == nil {
				term.Write([]byte("\x1b[31m[GAGAL] Modul .ko tidak ditemukan di dalam Zip!\x1b[0m\n"))
				status.Text = "File Hilang"; status.Refresh()
				return
			}

			term.Write([]byte(fmt.Sprintf("\x1b[32m[+] Menggunakan File: %s\x1b[0m\n", fileToExtract.Name)))
			rc, _ := fileToExtract.Open()
			buf := new(bytes.Buffer); io.Copy(buf, rc); rc.Close()
			userTmp := filepath.Join(os.TempDir(), "temp_mod.ko")
			os.WriteFile(userTmp, buf.Bytes(), 0644)
			exec.Command("su", "-c", fmt.Sprintf("cp %s %s", userTmp, targetKoPath)).Run()
			exec.Command("su", "-c", "chmod 777 "+targetKoPath).Run()
			os.Remove(userTmp) 

			term.Write([]byte("\x1b[36m[*] Memasang Modul (Inject)...\x1b[0m\n"))
			cmdInsmod := exec.Command("su", "-c", "insmod "+targetKoPath)
			output, err := cmdInsmod.CombinedOutput()
			outputStr := string(output)

			if err == nil {
				term.Write([]byte("\x1b[92m[SUKSES] Driver Berhasil Di install\x1b[0m\n"))
				lblKernelValue.Text = "ACTIVE"; lblKernelValue.Color = successGreen
				status.Text = "Berhasil Install"
			} else if strings.Contains(outputStr, "File exists") {
				term.Write([]byte("\x1b[33m[INFO] Driver Sudah Ada\x1b[0m\n"))
				lblKernelValue.Text = "ACTIVE"; lblKernelValue.Color = successGreen
				status.Text = "Sudah Aktif"
			} else {
				term.Write([]byte("\x1b[31m[GAGAL] Gagal install\x1b[0m\n"))
				term.Write([]byte("\x1b[31m" + outputStr + "\x1b[0m\n"))
				lblKernelValue.Text = "ERROR"; lblKernelValue.Color = failRed
				status.Text = "Gagal Install"
			}
			lblKernelValue.Refresh(); status.Refresh()
		}()
	}

	var checkUpdate func()
	checkUpdate = func() {
		overlayContainer.Hide()
		time.Sleep(500 * time.Millisecond)
		if strings.Contains(ConfigURL, "GANTI") { term.Write([]byte("\n\x1b[33m[WARN] ConfigURL!\x1b[0m\n")); return }
		term.Write([]byte("\n\x1b[90m[*] Checking updates...\x1b[0m\n"))
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(fmt.Sprintf("%s?v=%d", ConfigURL, time.Now().Unix()))
		if err == nil && resp.StatusCode == 200 {
			body, _ := io.ReadAll(resp.Body); resp.Body.Close()
			if dec, err := decryptConfig(string(bytes.TrimSpace(body))); err == nil {
				var cfg OnlineConfig
				if json.Unmarshal(dec, &cfg) == nil {
					if cfg.Version != "" && cfg.Version != AppVersion {
						showModal("UPDATE", cfg.Message, "UPDATE", func() { if u, e := url.Parse(cfg.Link); e == nil { app.New().OpenURL(u) } }, false, true)
					} else {
						term.Write([]byte("\x1b[32m[V] System Updated.\x1b[0m\n"))
					}
				}
			}
		} else {
			showModal("ERROR", "Gagal terhubung ke server.\nPeriksa koneksi internet.", "COBA LAGI", func() { go checkUpdate() }, true, true)
		}
	}

	go func() { time.Sleep(1500 * time.Millisecond); checkUpdate() }()

	titleText := createLabel("TANGSAN EXECUTOR", color.White, 16, true)
	infoGrid := container.NewGridWithColumns(3, container.NewVBox(lblKernelTitle, lblKernelValue), container.NewVBox(lblSELinuxTitle, lblSELinuxValue), container.NewVBox(lblSystemTitle, lblSystemValue))
	btnInj := widget.NewButtonWithIcon("Inject", theme.DownloadIcon(), func() { showModal("INJECT", "Mulai Inject Driver?", "MULAI", autoInstallKernel, false, false) })
	btnInj.Importance = widget.HighImportance
	btnSel := widget.NewButtonWithIcon("SELinux", theme.ViewRefreshIcon(), func() { go func() { if CheckSELinux() == "Enforcing" { exec.Command("su","-c","setenforce 0").Run() } else { exec.Command("su","-c","setenforce 1").Run() }; time.Sleep(100 * time.Millisecond); se := CheckSELinux(); lblSELinuxValue.Text = strings.ToUpper(se); if se == "Enforcing" { lblSELinuxValue.Color = successGreen } else if se == "Permissive" { lblSELinuxValue.Color = failRed } else { lblSELinuxValue.Color = color.Gray{Y: 150} }; lblSELinuxValue.Refresh() }() })
	btnSel.Importance = widget.HighImportance
	btnClr := widget.NewButtonWithIcon("Clear", theme.ContentClearIcon(), func() { term.Clear() })
	btnClr.Importance = widget.DangerImportance
	
	headerContent := container.NewVBox(container.NewPadded(titleText), container.NewPadded(infoGrid), container.NewPadded(container.NewGridWithColumns(3, btnInj, btnSel, btnClr)), container.NewPadded(status), widget.NewSeparator())
	header := container.NewStack(canvas.NewRectangle(color.Gray{Y: 45}), headerContent)

	sendBtn := widget.NewButtonWithIcon("", theme.MailSendIcon(), send)
	cpyLbl := createLabel("Code by TANGSAN", silverColor, 10, false)
	bottom := container.NewVBox(container.NewPadded(cpyLbl), container.NewPadded(container.NewBorder(nil, nil, nil, sendBtn, input)))
	bg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "bg.png", StaticContent: bgPng}); bg.FillMode = canvas.ImageFillStretch
	termBox := container.NewStack(canvas.NewRectangle(color.Black), bg, canvas.NewRectangle(color.RGBA{0,0,0,180}), term.scroll)
	
	fdImg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "fd.png", StaticContent: fdPng})
	fdImg.FillMode = canvas.ImageFillContain
	fdBtn := widget.NewButton("", func() { dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) { if r != nil { runFile(r) } }, w).Show() })
	fdBtn.Importance = widget.LowImportance
	fabWrapper := container.NewGridWrap(fyne.NewSize(65,65), container.NewStack(container.NewPadded(fdImg), fdBtn))
	fab := container.NewVBox(layout.NewSpacer(), container.NewPadded(container.NewHBox(layout.NewSpacer(), fabWrapper)), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "))

	overlayContainer = container.NewStack(); overlayContainer.Hide()
	w.SetContent(container.NewStack(container.NewBorder(header, bottom, nil, nil, termBox), fab, overlayContainer))
	w.ShowAndRun()
}

