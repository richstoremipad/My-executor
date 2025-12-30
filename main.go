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
	"unicode/utf8"

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
const AppVersion = "1.1" // Versi Fix TUI & UTF8
const ConfigURL = "https://raw.githubusercontent.com/tangsanrich/Fileku/main/executor.txt"
const CryptoKey = "RahasiaNegaraJanganSampaiBocorir"
const MaxScrollback = 100 

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

//go:embed driver.zip
var driverZip []byte

/* ==========================================
   CUSTOM THEME (FORCE MONOSPACE FOR TUI)
========================================== */
type MyTheme struct{}

func (m MyTheme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	return theme.DefaultTheme().Color(n, v)
}
func (m MyTheme) Font(s fyne.TextStyle) fyne.Resource {
	// PAKSA SEMUA TEKS JADI MONOSPACE AGAR KOTAK TUI LURUS
	return theme.DefaultTheme().Font(fyne.TextStyle{Monospace: true})
}
func (m MyTheme) Icon(n fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(n)
}
func (m MyTheme) Size(n fyne.ThemeSizeName) float32 {
	return theme.DefaultTheme().Size(n)
}

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
   TERMINAL EMULATOR (UTF-8 SUPPORT)
========================================== */
type Terminal struct {
	grid         *widget.TextGrid
	scroll       *container.Scroll
	curRow       int
	curCol       int
	curStyle     *widget.CustomTextGridStyle
	mutex        sync.Mutex
	needsRefresh bool
	
	// Buffer state
	escBuffer    []byte
	inEsc        bool
	utf8Buffer   []byte // Penampung byte sisa UTF-8
}

func NewTerminal() *Terminal {
	g := widget.NewTextGrid()
	g.ShowLineNumbers = false
	
	defStyle := &widget.CustomTextGridStyle{
		FGColor: color.White, // Default text putih
		BGColor: color.Black, // Default bg hitam pekat
	}

	term := &Terminal{
		grid:         g,
		scroll:       container.NewScroll(g),
		curRow:       0,
		curCol:       0,
		curStyle:     defStyle,
		inEsc:        false,
		escBuffer:    make([]byte, 0, 100),
		utf8Buffer:   make([]byte, 0, 10),
		needsRefresh: false,
	}

	term.ensureSize(40, 100)

	go func() {
		ticker := time.NewTicker(16 * time.Millisecond)
		for range ticker.C {
			term.mutex.Lock()
			if term.needsRefresh {
				term.grid.Refresh()
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
	t.ensureSize(40, 100)
	t.curRow = 0
	t.curCol = 0
	t.needsRefresh = true
	t.mutex.Unlock()
}

// FUNGSI UTAMA: MENANGANI BYTES DENGAN UTF-8 DECODING
func (t *Terminal) Write(p []byte) (int, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	// Gabungkan sisa buffer lama dengan data baru
	data := append(t.utf8Buffer, p...)
	t.utf8Buffer = t.utf8Buffer[:0] // Kosongkan buffer

	i := 0
	for i < len(data) {
		// 1. Cek apakah byte ini awal dari ESC Sequence
		if !t.inEsc && data[i] == 0x1b {
			t.inEsc = true
			t.escBuffer = t.escBuffer[:0]
			i++
			continue
		}

		// 2. Jika sedang dalam mode Escape (Color/Cursor)
		if t.inEsc {
			b := data[i]
			t.escBuffer = append(t.escBuffer, b)
			i++
			
			// Deteksi akhir sequence (huruf a-z, A-Z, @, `)
			if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '@' || b == '`' {
				t.handleCSI(t.escBuffer)
				t.inEsc = false
			}
			if len(t.escBuffer) > 50 { t.inEsc = false } // Safety break
			continue
		}

		// 3. Decode UTF-8 Rune (Perbaikan "â00")
		r, size := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && size == 1 {
			// Jika byte tidak lengkap, simpan ke buffer untuk paket berikutnya
			if len(data)-i < 4 { // Max UTF8 size is 4
				t.utf8Buffer = append(t.utf8Buffer, data[i:]...)
				break 
			}
			// Jika memang error, skip byte ini
			i++ 
			continue
		}
		
		// Proses Rune yang valid
		i += size
		
		switch r {
		case '\n': t.curRow++
		case '\r': t.curCol = 0
		case '\b': if t.curCol > 0 { t.curCol-- }
		case '\t': t.curCol += 4
		case 0x07: // Bell
		default:
			t.setChar(r)
			t.curCol++
		}
	}
	t.needsRefresh = true
	return len(p), nil
}

// LOGIC WARNA & POSISI (SAMA TAPI DIOPTIMALKAN)
func (t *Terminal) handleCSI(seq []byte) {
	if len(seq) < 1 || seq[0] != '[' { return }
	cmd := seq[len(seq)-1]
	paramsStr := string(seq[1 : len(seq)-1])
	
	var params []int
	if len(paramsStr) > 0 {
		cleanParams := strings.TrimPrefix(paramsStr, "?")
		for _, p := range strings.Split(cleanParams, ";") {
			var val int
			fmt.Sscanf(p, "%d", &val)
			params = append(params, val)
		}
	} else { params = []int{0} }

	getParam := func(idx, def int) int {
		if idx < len(params) { if params[idx] == 0 { return def }; return params[idx] }
		return def
	}

	switch cmd {
	case 'm': // Colors
		for i := 0; i < len(params); i++ {
			c := params[i]
			switch c {
			case 0: // Reset
				t.curStyle.FGColor = color.White
				t.curStyle.BGColor = color.Black
				t.curStyle.TextStyle = fyne.TextStyle{Monospace: true}
			case 1: t.curStyle.TextStyle.Bold = true
			case 30,31,32,33,34,35,36,37,90,91,92,93,94,95,96,97: 
				t.curStyle.FGColor = ansiToColor(c)
			case 39: t.curStyle.FGColor = color.White
			case 40,41,42,43,44,45,46,47: 
				t.curStyle.BGColor = ansiToColor(c-10)
			case 49: t.curStyle.BGColor = color.Black
			case 38, 48: // RGB Support (Simplifikasi)
				if i+2 < len(params) && params[i+1] == 5 {
					col := ansi256ToColor(params[i+2])
					if c == 38 { t.curStyle.FGColor = col } else { t.curStyle.BGColor = col }
					i += 2
				}
			}
		}
	case 'H', 'f': // Cursor Pos
		t.curRow = getParam(0, 1) - 1; t.curCol = getParam(1, 1) - 1
		t.ensureSize(t.curRow+1, t.curCol+1)
	case 'A': t.curRow -= getParam(0, 1); if t.curRow < 0 { t.curRow = 0 }
	case 'B': t.curRow += getParam(0, 1); t.ensureSize(t.curRow+1, t.curCol)
	case 'C': t.curCol += getParam(0, 1)
	case 'D': t.curCol -= getParam(0, 1); if t.curCol < 0 { t.curCol = 0 }
	case 'J': // Clear
		if getParam(0, 0) == 2 {
			t.grid.SetText(""); t.curRow = 0; t.curCol = 0
			t.ensureSize(40, 100)
		}
	case 'K': // Clear Line
		t.ensureSize(t.curRow+1, t.curCol+1)
		rowCells := t.grid.Rows[t.curRow].Cells
		if t.curCol < len(rowCells) {
			t.grid.SetRow(t.curRow, widget.TextGridRow{Cells: rowCells[:t.curCol]})
		}
	}
}

func (t *Terminal) ensureSize(rows, cols int) {
	for len(t.grid.Rows) <= rows {
		t.grid.SetRow(len(t.grid.Rows), widget.TextGridRow{Cells: []widget.TextGridCell{}})
	}
}

func (t *Terminal) setChar(r rune) {
	t.ensureSize(t.curRow, t.curCol)
	rowCells := t.grid.Rows[t.curRow].Cells
	
	if t.curCol >= len(rowCells) {
		newCells := make([]widget.TextGridCell, t.curCol+1)
		copy(newCells, rowCells)
		for i := len(rowCells); i < t.curCol; i++ { 
			newCells[i] = widget.TextGridCell{Rune: ' ', Style: t.curStyle} 
		}
		rowCells = newCells
	}
	
	styleCopy := *t.curStyle
	// Paksa Monospace agar karakter TUI lebar nya sama
	styleCopy.TextStyle.Monospace = true 
	
	rowCells[t.curCol] = widget.TextGridCell{Rune: r, Style: &styleCopy}
	t.grid.SetRow(t.curRow, widget.TextGridRow{Cells: rowCells})
}

// WARNA YANG LEBIH CERAH UNTUK TUI
func ansiToColor(c int) color.Color {
	switch c {
	case 30, 90: return color.RGBA{100, 100, 100, 255}
	case 31, 91: return color.RGBA{255, 85, 85, 255}
	case 32, 92: return color.RGBA{80, 250, 123, 255}
	case 33, 93: return color.RGBA{241, 250, 140, 255}
	case 34, 94: return color.RGBA{189, 147, 249, 255}
	case 35, 95: return color.RGBA{255, 121, 198, 255}
	case 36, 96: return color.RGBA{139, 233, 253, 255}
	case 37, 97: return color.White
	default: return color.White
	}
}

func ansi256ToColor(idx int) color.Color {
	if idx < 16 { return ansiToColor(30 + idx) }
	if idx == 232 { return color.Black }
	if idx >= 232 && idx <= 255 {
		gray := uint8((idx - 232) * 10 + 8)
		return color.RGBA{gray, gray, gray, 255}
	}
	return color.White
}

/* ===============================
   SYSTEM HELPERS
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
	
	// PENTING: SET CUSTOM THEME AGAR FONT MONOSPACE
	a.Settings().SetTheme(&MyTheme{})

	w := a.NewWindow("TANGSAN EXECUTOR (TUI FIXED)")
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

	executeTask := func(cmdText string, isScript bool, scriptPath string, isBinary bool) {
		status.Text = "Status: Running TUI..."
		status.Refresh()

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

			// ENV untuk TUI
			cmd.Env = append(os.Environ(), 
				"TERM=xterm-256color",
				"COLORTERM=truecolor",
				"LANG=en_US.UTF-8",
			)

			ptmx, err := pty.Start(cmd)
			if err != nil {
				status.Text = "Error"; status.Refresh(); return
			}

			// UKURAN TERMINAL FIXED AGAR PAS
			pty.Setsize(ptmx, &pty.Winsize{Rows: 35, Cols: 100, X: 0, Y: 0})

			cmdMutex.Lock(); activeStdin = ptmx; cmdMutex.Unlock()

			defer func() {
				_ = ptmx.Close()
				cmdMutex.Lock(); activeStdin = nil; cmdMutex.Unlock()
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
		if activeStdin != nil { activeStdin.Write([]byte(text + "\n")); cmdMutex.Unlock(); return }
		cmdMutex.Unlock()
		if strings.TrimSpace(text) == "" { return }
		
		if strings.HasPrefix(text, "cd") {
			parts := strings.Fields(text)
			newPath := currentDir
			if len(parts) > 1 { newPath = filepath.Join(currentDir, parts[1]) }
			if CheckRoot() { if exec.Command("su", "-c", "[ -d \""+newPath+"\" ]").Run() == nil { currentDir = newPath } } 
			term.Write([]byte(fmt.Sprintf("cd %s\n", newPath)))
			return
		}
		executeTask(text, false, "", false)
	}
	input.OnSubmitted = func(_ string) { send() }

	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close(); term.Clear()
		data, err := io.ReadAll(reader)
		if err != nil { return }
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))
		tmpFile, err := os.CreateTemp("", "exec_tmp")
		if err != nil { return }
		tmpPath := tmpFile.Name(); tmpFile.Write(data); tmpFile.Close(); os.Chmod(tmpPath, 0755)
		executeTask("", true, tmpPath, isBinary)
	}

	var overlayContainer *fyne.Container
	
	showModal := func(title, msg, confirm string, action func(), isErr bool, isForce bool) {
		w.Canvas().Refresh(w.Content())
		cancelLabel := "BATAL"; cancelFunc := func() { overlayContainer.Hide() }
		if isForce { cancelLabel = "KELUAR"; cancelFunc = func() { os.Exit(0) } }
		
		btnCancel := widget.NewButton(cancelLabel, cancelFunc)
		btnCancel.Importance = widget.DangerImportance
		btnOk := widget.NewButton(confirm, func() { if !isForce { overlayContainer.Hide() }; if action != nil { action() } })
		if confirm == "COBA LAGI" { btnOk.Importance = widget.HighImportance } else { if isErr { btnOk.Importance = widget.DangerImportance } else { btnOk.Importance = widget.HighImportance } }
		
		btnBox := container.NewHBox(layout.NewSpacer(), container.NewGridWrap(fyne.NewSize(110,40), btnCancel), widget.NewLabel("   "), container.NewGridWrap(fyne.NewSize(110,40), btnOk), layout.NewSpacer())
		lblTitle := createLabel(title, theme.ForegroundColor(), 18, true); if isErr { lblTitle.Color = theme.ErrorColor() }
		lblMsg := widget.NewLabel(msg); lblMsg.Alignment = fyne.TextAlignCenter; lblMsg.Wrapping = fyne.TextWrapWord
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
			targetKoPath := "/data/local/tmp/module_inject.ko"
			defer func() { exec.Command("su", "-c", "rm -f "+targetKoPath).Run() }()
			
			term.Write([]byte("Checking Kernel...\n"))
			out, _ := exec.Command("uname", "-r").Output()
			targetVer := strings.Split(strings.TrimSpace(string(out)), "-")[0]
			
			zipReader, err := zip.NewReader(bytes.NewReader(driverZip), int64(len(driverZip)))
			if err != nil { term.Write([]byte("Zip Error\n")); return }
			
			var fileToExtract *zip.File
			for _, f := range zipReader.File { if strings.Contains(f.Name, targetVer) && strings.HasSuffix(f.Name, ".ko") { fileToExtract = f; break } }
			if fileToExtract == nil { term.Write([]byte("Driver not found\n")); return }
			
			rc, _ := fileToExtract.Open()
			buf := new(bytes.Buffer); io.Copy(buf, rc); rc.Close()
			userTmp := filepath.Join(os.TempDir(), "temp_mod.ko")
			os.WriteFile(userTmp, buf.Bytes(), 0644)
			exec.Command("su", "-c", fmt.Sprintf("cp %s %s && chmod 777 %s", userTmp, targetKoPath, targetKoPath)).Run()
			os.Remove(userTmp) 
			
			if exec.Command("su", "-c", "insmod "+targetKoPath).Run() == nil {
				lblKernelValue.Text = "ACTIVE"; lblKernelValue.Color = successGreen
			} else {
				lblKernelValue.Text = "ERROR"; lblKernelValue.Color = failRed
			}
			lblKernelValue.Refresh(); status.Refresh()
		}()
	}

	var checkUpdate func()
	checkUpdate = func() {
		overlayContainer.Hide()
		time.Sleep(500 * time.Millisecond)
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(fmt.Sprintf("%s?v=%d", ConfigURL, time.Now().Unix()))
		if err == nil && resp.StatusCode == 200 {
			body, _ := io.ReadAll(resp.Body); resp.Body.Close()
			if dec, err := decryptConfig(string(bytes.TrimSpace(body))); err == nil {
				var cfg OnlineConfig
				if json.Unmarshal(dec, &cfg) == nil && cfg.Version != "" && cfg.Version != AppVersion {
					showModal("UPDATE", cfg.Message, "UPDATE", func() { if u, e := url.Parse(cfg.Link); e == nil { app.New().OpenURL(u) } }, false, true)
				} else { term.Write([]byte("\x1b[32m[V] Updated.\x1b[0m\n")) }
			}
		} else {
			showModal("ERROR", "Connection Failed", "COBA LAGI", func() { go checkUpdate() }, true, true)
		}
	}
	go func() { time.Sleep(1500 * time.Millisecond); checkUpdate() }()

	titleText := createLabel("TANGSAN EXECUTOR", color.White, 16, true)
	infoGrid := container.NewGridWithColumns(3, container.NewVBox(lblKernelTitle, lblKernelValue), container.NewVBox(lblSELinuxTitle, lblSELinuxValue), container.NewVBox(lblSystemTitle, lblSystemValue))
	btnInj := widget.NewButtonWithIcon("Inject", theme.DownloadIcon(), func() { showModal("INJECT", "Inject Driver?", "MULAI", autoInstallKernel, false, false) })
	btnInj.Importance = widget.HighImportance
	btnSel := widget.NewButtonWithIcon("SELinux", theme.ViewRefreshIcon(), func() { go func() { if CheckSELinux() == "Enforcing" { exec.Command("su","-c","setenforce 0").Run() } else { exec.Command("su","-c","setenforce 1").Run() }; time.Sleep(100 * time.Millisecond); se := CheckSELinux(); lblSELinuxValue.Text = strings.ToUpper(se); if se == "Enforcing" { lblSELinuxValue.Color = successGreen } else { lblSELinuxValue.Color = failRed }; lblSELinuxValue.Refresh() }() })
	btnSel.Importance = widget.HighImportance
	btnClr := widget.NewButtonWithIcon("Clear", theme.ContentClearIcon(), func() { term.Clear() })
	btnClr.Importance = widget.DangerImportance
	
	headerContent := container.NewVBox(container.NewPadded(titleText), container.NewPadded(infoGrid), container.NewPadded(container.NewGridWithColumns(3, btnInj, btnSel, btnClr)), container.NewPadded(status), widget.NewSeparator())
	header := container.NewStack(canvas.NewRectangle(color.Gray{Y: 45}), headerContent)

	sendBtn := widget.NewButtonWithIcon("", theme.MailSendIcon(), send)
	cpyLbl := createLabel("Code by TANGSAN", silverColor, 10, false)
	bottom := container.NewVBox(container.NewPadded(cpyLbl), container.NewPadded(container.NewBorder(nil, nil, nil, sendBtn, input)))
	
	// BACKGROUND DIHILANGKAN AGAR TUI BERSIH (HANYA HITAM SOLID)
	termBox := container.NewStack(canvas.NewRectangle(color.Black), term.scroll)
	
	fdImg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "fd.png", StaticContent: fdPng}); fdImg.FillMode = canvas.ImageFillContain
	fdBtn := widget.NewButton("", func() { dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) { if r != nil { runFile(r) } }, w).Show() }); fdBtn.Importance = widget.LowImportance
	fabWrapper := container.NewGridWrap(fyne.NewSize(65,65), container.NewStack(container.NewPadded(fdImg), fdBtn))
	fab := container.NewVBox(layout.NewSpacer(), container.NewPadded(container.NewHBox(layout.NewSpacer(), fabWrapper)), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "))

	overlayContainer = container.NewStack(); overlayContainer.Hide()
	w.SetContent(container.NewStack(container.NewBorder(header, bottom, nil, nil, termBox), fab, overlayContainer))
	w.ShowAndRun()
}

