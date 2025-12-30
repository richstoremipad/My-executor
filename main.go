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
	"regexp"
	"strings"
	"sync"
	"time"

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
   CONFIG
========================================== */
const AppVersion = "2.0-STABLE"
const ConfigURL = "https://raw.githubusercontent.com/tangsanrich/Fileku/main/executor.txt"
const CryptoKey = "RahasiaNegaraJanganSampaiBocorir"

// HANYA TAMPILKAN 50 BARIS TERAKHIR (Agar hemat Memori & CPU)
const MaxDisplayRows = 50 
// UPDATE LAYAR 10 KALI PER DETIK (Cukup untuk mata manusia, sangat ringan untuk CPU)
const RefreshRate = 100 * time.Millisecond 

var currentDir string = "/sdcard"
var activeStdin io.WriteCloser
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
   HELPER / SECURITY
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

/* ==========================================
   TERMINAL ENGINE (ANTI-LAG)
========================================== */
type Terminal struct {
	grid          *widget.TextGrid
	scroll        *container.Scroll
	
	// Buffer Data Mentah
	rawBuffer     bytes.Buffer
	bufMutex      sync.Mutex
	
	// Cache Baris yang sudah di-parse (Siap Render)
	cachedRows    []widget.TextGridRow
	
	reAnsi        *regexp.Regexp
	OnNavRequired func() 
}

func NewTerminal() *Terminal {
	g := widget.NewTextGrid()
	g.ShowLineNumbers = false
	
	term := &Terminal{
		grid:       g,
		scroll:     container.NewScroll(g),
		reAnsi:     regexp.MustCompile(`\x1b\[([0-9;]*)?([a-zA-Z])`),
		cachedRows: make([]widget.TextGridRow, 0, MaxDisplayRows),
	}

	// LOOP RENDER TERPISAH (GOROUTINE)
	go term.renderLoop()
	
	return term
}

// Write hanya menampung data ke buffer (Sangat Cepat, Non-Blocking)
func (t *Terminal) Write(p []byte) (int, error) {
	t.bufMutex.Lock()
	// Batasi ukuran buffer mentah agar RAM tidak meledak
	if t.rawBuffer.Len() > 20000 { 
		t.rawBuffer.Reset() // Drop data lama jika terlalu penuh (Tail Drop)
	}
	t.rawBuffer.Write(p)
	t.bufMutex.Unlock()
	
	// Cek navigasi (Sampling cepat)
	if len(p) > 0 {
		str := string(p)
		if strings.Contains(strings.ToLower(str), "navigasi") || strings.Contains(strings.ToLower(str), "menu") {
			if t.OnNavRequired != nil { go t.OnNavRequired() }
		}
	}
	return len(p), nil
}

func (t *Terminal) Clear() {
	t.bufMutex.Lock()
	t.rawBuffer.Reset()
	t.cachedRows = []widget.TextGridRow{} // Reset Cache
	t.bufMutex.Unlock()
	
	// Update UI segera
	t.grid.SetText("")
}

// Loop Utama: Bangun UI setiap 100ms
func (t *Terminal) renderLoop() {
	ticker := time.NewTicker(RefreshRate)
	defer ticker.Stop()

	for range ticker.C {
		t.processBuffer()
	}
}

func (t *Terminal) processBuffer() {
	t.bufMutex.Lock()
	if t.rawBuffer.Len() == 0 {
		t.bufMutex.Unlock()
		return
	}
	// Ambil SEMUA data yang tertunda
	data := t.rawBuffer.String()
	t.rawBuffer.Reset()
	t.bufMutex.Unlock()

	// Jika data sangat besar (misal 9MB sekaligus), kita hanya ambil 4000 karakter terakhir
	// Ini kuncinya agar aplikasi tidak hang
	if len(data) > 4000 {
		data = "...\n[OUTPUT TERLALU CEPAT - DATA LAMA DIHAPUS]\n..." + data[len(data)-4000:]
	}

	// Parsing ANSI menjadi Rows (Struktur Data)
	newRows := t.parseAnsiToRows(data)

	// Gabungkan dengan cache baris yang ada
	t.cachedRows = append(t.cachedRows, newRows...)
	
	// Potong agar hanya menyimpan MaxDisplayRows (misal 50 baris terakhir)
	if len(t.cachedRows) > MaxDisplayRows {
		t.cachedRows = t.cachedRows[len(t.cachedRows)-MaxDisplayRows:]
	}

	// UPDATE UI DI MAIN THREAD
	// Kita copy slice agar aman thread-safe
	finalRows := make([]widget.TextGridRow, len(t.cachedRows))
	copy(finalRows, t.cachedRows)
	
	t.grid.Rows = finalRows // Swap Pointer (Atomic-like)
	t.grid.Refresh()
	t.scroll.ScrollToBottom()
}

// Logika Parsing Warna yang dioptimalkan
func (t *Terminal) parseAnsiToRows(text string) []widget.TextGridRow {
	var rows []widget.TextGridRow
	
	// Normalisasi baris
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")

	// Style Default
	currentStyle := &widget.CustomTextGridStyle{FGColor: theme.ForegroundColor(), BGColor: color.Transparent}

	for _, line := range lines {
		// Jika baris kosong skip (kecuali mau enter)
		// if line == "" { continue } 
		
		row := widget.TextGridRow{Cells: []widget.TextGridCell{}}
		
		// Parsing ANSI dalam baris ini
		remaining := line
		for len(remaining) > 0 {
			loc := t.reAnsi.FindStringIndex(remaining)
			
			// Teks biasa sebelum kode ANSI
			if loc == nil {
				row.Cells = append(row.Cells, t.stringToCells(remaining, currentStyle)...)
				break
			}
			if loc[0] > 0 {
				row.Cells = append(row.Cells, t.stringToCells(remaining[:loc[0]], currentStyle)...)
			}
			
			// Proses Kode ANSI
			ansiCode := remaining[loc[0]:loc[1]]
			t.updateStyle(ansiCode, currentStyle)
			
			remaining = remaining[loc[1]:]
		}
		rows = append(rows, row)
	}
	return rows
}

func (t *Terminal) stringToCells(s string, style *widget.CustomTextGridStyle) []widget.TextGridCell {
	cells := make([]widget.TextGridCell, len(s))
	// Copy value style agar tidak berubah pointer-nya nanti
	staticStyle := *style 
	for i, r := range s {
		cells[i] = widget.TextGridCell{Rune: r, Style: &staticStyle}
	}
	return cells
}

func (t *Terminal) updateStyle(codeSeq string, style *widget.CustomTextGridStyle) {
	if len(codeSeq) < 3 { return }
	content := codeSeq[2 : len(codeSeq)-1]
	
	parts := strings.Split(content, ";")
	for _, part := range parts {
		if part == "" || part == "0" {
			style.FGColor = theme.ForegroundColor() // Reset
		} else {
			col := ansiToColor(part)
			if col != nil { style.FGColor = col }
		}
	}
}

func ansiToColor(code string) color.Color {
	switch code {
	case "30", "90": return color.Gray{Y: 100}
	case "31", "91": return theme.ErrorColor()
	case "32", "92": return theme.SuccessColor()
	case "33", "93": return theme.WarningColor()
	case "34", "94": return theme.PrimaryColor()
	case "35", "95": return color.RGBA{R: 200, G: 0, B: 200, A: 255}
	case "36", "96": return color.RGBA{R: 0, G: 255, B: 255, A: 255}
	case "37", "97": return color.White
	default: return nil
	}
}

/* ===============================
   UI UTAMA
================================ */
func main() {
	a := app.New()
	a.Settings().SetTheme(theme.DarkTheme())

	w := a.NewWindow("Executor Stable")
	w.Resize(fyne.NewSize(400, 700))
	w.SetMaster()

	term := NewTerminal()
	
	// Auto Root Check
	go func() {
		time.Sleep(1 * time.Second)
		if !CheckRoot() { /* Info */ }
	}()
	if !CheckRoot() { currentDir = "/sdcard" }

	// UI Components
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

	// Status Updater
	go func() {
		time.Sleep(1 * time.Second)
		for {
			func() {
				defer func() { if r := recover(); r != nil {} }()
				if CheckRoot() { lblSystemValue.Text = "GRANTED"; lblSystemValue.Color = successGreen } else { lblSystemValue.Text = "DENIED"; lblSystemValue.Color = failRed }
				lblSystemValue.Refresh()
				if CheckKernelDriver() { lblKernelValue.Text = "ACTIVE"; lblKernelValue.Color = successGreen } else { lblKernelValue.Text = "MISSING"; lblKernelValue.Color = failRed }
				lblKernelValue.Refresh()
				se := CheckSELinux(); lblSELinuxValue.Text = strings.ToUpper(se)
				if se == "Enforcing" { lblSELinuxValue.Color = successGreen } else { lblSELinuxValue.Color = failRed }
				lblSELinuxValue.Refresh()
			}()
			time.Sleep(3 * time.Second)
		}
	}()

	executeTask := func(cmdText string, isScript bool, scriptPath string, isBinary bool) {
		status.Text = "Status: Processing..."
		status.Refresh()

		if !isScript {
			displayDir := currentDir
			if len(displayDir) > 20 { displayDir = "..." + displayDir[len(displayDir)-15:] }
			term.Write([]byte(fmt.Sprintf("\x1b[33m%s > \x1b[0m%s\n", displayDir, cmdText)))
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
					cmd = exec.Command("sh", "-c", cmdText) // Simple sh
					cmd.Dir = currentDir
				}
			}

			// PIPING SYSTEM
			cmd.Env = append(os.Environ(), "TERM=xterm-256color")
			
			// Buat Pipe manual agar bisa handle data besar
			prOut, pwOut := io.Pipe()
			prErr, pwErr := io.Pipe()
			cmd.Stdout = pwOut
			cmd.Stderr = pwErr
			
			stdinPipe, _ := cmd.StdinPipe()
			cmdMutex.Lock(); activeStdin = stdinPipe; cmdMutex.Unlock()

			// Reader Goroutine (Cepat)
			readerFunc := func(r io.Reader) {
				buf := make([]byte, 8192) // Buffer 8KB
				for {
					n, err := r.Read(buf)
					if n > 0 {
						term.Write(buf[:n]) // Kirim ke terminal engine
					}
					if err != nil { break }
				}
			}

			if err := cmd.Start(); err != nil {
				term.Write([]byte(fmt.Sprintf("\x1b[31mError: %s\x1b[0m\n", err.Error())))
				cmdMutex.Lock(); activeStdin = nil; cmdMutex.Unlock()
				return
			}
			
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); readerFunc(prOut) }()
			go func() { defer wg.Done(); readerFunc(prErr) }()
			
			wg.Wait(); cmd.Wait()
			pwOut.Close(); pwErr.Close()

			cmdMutex.Lock(); activeStdin = nil; cmdMutex.Unlock()
			status.Text = "Status: Idle"; status.Refresh()
			if isScript && isRoot { exec.Command("su", "-c", "rm -f /data/local/tmp/temp_exec").Run() }
		}()
	}

	send := func() {
		text := input.Text; input.SetText("")
		cmdMutex.Lock()
		if activeStdin != nil { io.WriteString(activeStdin, text+"\n"); cmdMutex.Unlock(); return }
		cmdMutex.Unlock()
		if strings.TrimSpace(text) == "" { return }
		
		if strings.HasPrefix(text, "cd") {
			parts := strings.Fields(text)
			newPath := currentDir
			if len(parts) > 1 { newPath = filepath.Join(currentDir, parts[1]) }
			currentDir = newPath
			term.Write([]byte(fmt.Sprintf("\x1b[33mDir > \x1b[0m%s\n", currentDir)))
			return
		}
		executeTask(text, false, "", false)
	}
	input.OnSubmitted = func(_ string) { send() }

	// ================= NAVIGASI =================
	sendKey := func(data string) {
		cmdMutex.Lock(); defer cmdMutex.Unlock()
		if activeStdin != nil { io.WriteString(activeStdin, data) }
	}

	var navFloatContainer *fyne.Container
	btnUp := widget.NewButtonWithIcon("", theme.MoveUpIcon(), func() { sendKey("\x1b[A") })
	btnDown := widget.NewButtonWithIcon("", theme.MoveDownIcon(), func() { sendKey("\x1b[B") })
	btnEnter := widget.NewButton("ENTER", func() { sendKey("\n") })
	btnQ := widget.NewButton("Q", func() { sendKey("q") })
	btnCloseNav := widget.NewButtonWithIcon("", theme.CancelIcon(), func() { navFloatContainer.Hide() })
	btnCloseNav.Importance = widget.DangerImportance 

	navButtons := container.NewGridWithColumns(5, btnQ, btnUp, btnDown, btnEnter, btnCloseNav)
	navBg := canvas.NewRectangle(color.RGBA{R: 30, G: 30, B: 30, A: 230}); navBg.CornerRadius = 8
	navFloatContainer = container.NewVBox(layout.NewSpacer(), container.NewHBox(layout.NewSpacer(), container.NewGridWrap(fyne.NewSize(380, 50), container.NewStack(navBg, container.NewPadded(navButtons))), layout.NewSpacer()), widget.NewLabel(" "), widget.NewLabel(" "))
	navFloatContainer.Hide() 

	term.OnNavRequired = func() { navFloatContainer.Show(); navFloatContainer.Refresh() }

	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close(); term.Clear()
		data, err := io.ReadAll(reader)
		if err != nil { term.Write([]byte("Error Read")); return }
		tmpFile, _ := os.CreateTemp("", "exec_tmp"); tmpPath := tmpFile.Name()
		tmpFile.Write(data); tmpFile.Close(); os.Chmod(tmpPath, 0755)
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))
		executeTask("", true, tmpPath, isBinary)
	}

	// ================= LAYOUT =================
	infoGrid := container.NewGridWithColumns(3, container.NewVBox(lblKernelTitle, lblKernelValue), container.NewVBox(lblSELinuxTitle, lblSELinuxValue), container.NewVBox(lblSystemTitle, lblSystemValue))
	btnClr := widget.NewButtonWithIcon("Clear", theme.ContentClearIcon(), func() { term.Clear() }); btnClr.Importance = widget.DangerImportance
	
	header := container.NewStack(canvas.NewRectangle(color.Gray{Y: 45}), container.NewVBox(container.NewPadded(createLabel("EXECUTOR", color.White, 16, true)), container.NewPadded(infoGrid), container.NewPadded(btnClr), container.NewPadded(status), widget.NewSeparator()))
	
	bottomArea := container.NewVBox(container.NewPadded(createLabel("Code by TANGSAN", silverColor, 10, false)), container.NewPadded(container.NewBorder(nil, nil, nil, widget.NewButtonWithIcon("", theme.MailSendIcon(), send), input)))

	bg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "bg.png", StaticContent: bgPng}); bg.FillMode = canvas.ImageFillStretch
	termBox := container.NewStack(canvas.NewRectangle(color.Black), bg, canvas.NewRectangle(color.RGBA{0,0,0,180}), term.scroll)
	
	fdImg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "fd.png", StaticContent: fdPng}); fdImg.FillMode = canvas.ImageFillContain
	fdBtn := widget.NewButton("", func() { dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) { if r != nil { runFile(r) } }, w).Show() }); fdBtn.Importance = widget.LowImportance
	fab := container.NewVBox(layout.NewSpacer(), container.NewPadded(container.NewHBox(layout.NewSpacer(), container.NewGridWrap(fyne.NewSize(65,65), container.NewStack(container.NewPadded(fdImg), fdBtn)))), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "))

	w.SetContent(container.NewStack(container.NewBorder(header, bottomArea, nil, nil, termBox), fab, navFloatContainer))
	w.ShowAndRun()
}

func createLabel(text string, color color.Color, size float32, bold bool) *canvas.Text {
	lbl := canvas.NewText(text, color); lbl.TextSize = size; lbl.Alignment = fyne.TextAlignCenter; if bold { lbl.TextStyle = fyne.TextStyle{Bold: true} }; return lbl
}

func copyFile(src, dst string) error { in, _ := os.Open(src); defer in.Close(); out, _ := os.Create(dst); defer out.Close(); io.Copy(out, in); return nil }

