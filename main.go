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
	"fyne.io/fyne/v2/driver/mobile"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

/* ==========================================
   CONFIG & UPDATE SYSTEM
========================================== */
const AppVersion = "1.0"
const ConfigURL = "https://raw.githubusercontent.com/tangsanrich/Fileku/main/executor.txt"
const CryptoKey = "RahasiaNegaraJanganSampaiBocorir"

// BATAS MAX BARIS AGAR TIDAK LEMOT/CRASH
const MaxScrollback = 100 

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
   CUSTOM WIDGET: INTERACTIVE TEXT GRID
   (Untuk menangani Copy & Swipe Navigation)
========================================== */
type TouchableTextGrid struct {
	widget.TextGrid
	term       *Terminal
	lastDragY  float32
	dragAccum  float32
}

func NewTouchableTextGrid(t *Terminal) *TouchableTextGrid {
	g := &TouchableTextGrid{term: t}
	g.ExtendBaseWidget(g)
	g.ShowLineNumbers = false
	return g
}

// Fitur 1: Long Press / Klik Kanan untuk COPY
func (t *TouchableTextGrid) TappedSecondary(e *fyne.PointEvent) {
	t.copyContent()
}

// Support untuk Mobile Long Press
func (t *TouchableTextGrid) Tapped(e *fyne.PointEvent) {
	// Kosongkan agar tidak error, bisa diisi logika klik jika perlu
}

func (t *TouchableTextGrid) TouchDown(e *mobile.TouchEvent) {
	// Diperlukan untuk event touch mobile
}
func (t *TouchableTextGrid) TouchUp(e *mobile.TouchEvent) {
	// Diperlukan untuk event touch mobile
}
func (t *TouchableTextGrid) TouchCancel(e *mobile.TouchEvent) {
	// Diperlukan untuk event touch mobile
}

// Fitur 2: DRAG untuk NAVIGASI (Swipe)
func (t *TouchableTextGrid) OnDragStart() {
	t.dragAccum = 0
}

func (t *TouchableTextGrid) Dragged(e *fyne.DragEvent) {
	// Hitung pergerakan Y (Vertikal)
	t.dragAccum += e.Dragged.DY

	// Threshold sensivitas (setiap 20 pixel gerakan mengirim 1 tombol)
	threshold := float32(20.0)

	cmdMutex.Lock()
	defer cmdMutex.Unlock()

	// Hanya kirim tombol jika ada script berjalan (activeStdin != nil)
	if activeStdin != nil {
		if t.dragAccum > threshold {
			// Geser ke Bawah (Jari turun) -> Kirim Panah ATAS (Navigasi Naik)
			io.WriteString(activeStdin, "\x1b[A") 
			t.dragAccum = 0
		} else if t.dragAccum < -threshold {
			// Geser ke Atas (Jari naik) -> Kirim Panah BAWAH (Navigasi Turun)
			io.WriteString(activeStdin, "\x1b[B")
			t.dragAccum = 0
		}
	}
}

func (t *TouchableTextGrid) DragEnd() {
	t.dragAccum = 0
}

func (t *TouchableTextGrid) copyContent() {
	var content strings.Builder
	for _, row := range t.Rows {
		for _, cell := range row.Cells {
			content.WriteRune(cell.Rune)
		}
		content.WriteString("\n")
	}
	
	// Salin ke Clipboard
	clipboard := fyne.CurrentApp().Driver().AllWindows()[0].Clipboard()
	clipboard.SetContent(content.String())
	
	// Beri feedback visual (tulis ke terminal sebentar)
	// Kita inject pesan sistem ke terminal local
	t.term.printText("\n\x1b[32m[SYSTEM] Teks disalin ke Clipboard!\x1b[0m\n")
	t.term.needsRefresh = true
}

/* ==========================================
   TERMINAL LOGIC
========================================== */
type Terminal struct {
	grid         *TouchableTextGrid // Gunakan widget custom
	scroll       *container.Scroll
	curRow       int
	curCol       int
	curStyle     *widget.CustomTextGridStyle
	mutex        sync.Mutex
	reAnsi       *regexp.Regexp
	needsRefresh bool
}

func NewTerminal() *Terminal {
	// Inisialisasi struct dulu tanpa grid
	term := &Terminal{
		curRow:       0,
		curCol:       0,
		reAnsi:       regexp.MustCompile(`\x1b\[([0-9;]*)?([a-zA-Z])`),
		needsRefresh: false,
	}
	
	// Buat custom grid
	g := NewTouchableTextGrid(term)
	defStyle := &widget.CustomTextGridStyle{
		FGColor: theme.ForegroundColor(),
		BGColor: color.Transparent,
	}
	term.curStyle = defStyle
	term.grid = g
	
	// Scroll container
	term.scroll = container.NewScroll(g)

	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		for range ticker.C {
			term.mutex.Lock()
			if term.needsRefresh {
				term.grid.Refresh()
				term.scroll.ScrollToBottom()
				term.needsRefresh = false
			}
			term.mutex.Unlock()
		}
	}()
	return term
}

func ansiToColor(code string) color.Color {
	switch code {
	case "30": return color.Gray{Y: 100}
	case "31": return theme.ErrorColor()
	case "32": return theme.SuccessColor()
	case "33": return theme.WarningColor()
	case "34": return theme.PrimaryColor()
	case "35": return color.RGBA{R: 200, G: 0, B: 200, A: 255}
	case "36": return color.RGBA{R: 0, G: 255, B: 255, A: 255}
	case "37": return theme.ForegroundColor()
	case "90": return color.Gray{Y: 100}
	case "91": return color.RGBA{R: 255, G: 100, B: 100, A: 255}
	case "92": return color.RGBA{R: 100, G: 255, B: 100, A: 255}
	case "93": return color.RGBA{R: 255, G: 255, B: 100, A: 255}
	case "94": return color.RGBA{R: 100, G: 100, B: 255, A: 255}
	case "95": return color.RGBA{R: 255, G: 100, B: 255, A: 255}
	case "96": return color.RGBA{R: 100, G: 255, B: 255, A: 255}
	case "97": return color.White
	default: return nil
	}
}

func (t *Terminal) Clear() {
	t.mutex.Lock()
	t.grid.SetText("")
	t.curRow = 0; t.curCol = 0
	t.needsRefresh = true
	t.mutex.Unlock()
}

func (t *Terminal) Write(p []byte) (int, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	raw := string(p)
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	for len(raw) > 0 {
		loc := t.reAnsi.FindStringIndex(raw)
		if loc == nil { t.printText(raw); break }
		if loc[0] > 0 { t.printText(raw[:loc[0]]) }
		t.handleAnsiCode(raw[loc[0]:loc[1]])
		raw = raw[loc[1]:]
	}
	t.needsRefresh = true
	return len(p), nil
}

func (t *Terminal) handleAnsiCode(codeSeq string) {
	if len(codeSeq) < 3 { return }
	content := codeSeq[2 : len(codeSeq)-1]
	command := codeSeq[len(codeSeq)-1]
	switch command {
	case 'm':
		parts := strings.Split(content, ";")
		for _, part := range parts {
			if part == "" || part == "0" { t.curStyle.FGColor = theme.ForegroundColor() } else { col := ansiToColor(part); if col != nil { t.curStyle.FGColor = col } }
		}
	case 'J':
		if strings.Contains(content, "2") { t.grid.SetText(""); t.curRow = 0; t.curCol = 0 }
	case 'H': t.curRow = 0; t.curCol = 0
	}
}

func (t *Terminal) printText(text string) {
	for _, char := range text {
		if char == '\n' { 
			t.curRow++
			t.curCol = 0
			if len(t.grid.Rows) > MaxScrollback { t.grid.Rows = t.grid.Rows[1:]; t.curRow-- }
			continue 
		}
		if char == '\r' { t.curCol = 0; continue }
		for t.curRow >= len(t.grid.Rows) { t.grid.SetRow(len(t.grid.Rows), widget.TextGridRow{Cells: []widget.TextGridCell{}}) }
		rowCells := t.grid.Rows[t.curRow].Cells
		if t.curCol >= len(rowCells) {
			newCells := make([]widget.TextGridCell, t.curCol+1)
			copy(newCells, rowCells)
			t.grid.SetRow(t.curRow, widget.TextGridRow{Cells: newCells})
		}
		cellStyle := *t.curStyle
		t.grid.SetCell(t.curRow, t.curCol, widget.TextGridCell{Rune: char, Style: &cellStyle})
		t.curCol++
	}
}

/* ===============================
   SYSTEM HELPERS
================================ */
func drawProgressBar(term *Terminal, label string, percent int, colorCode string) {
	barLength := 20; filledLength := (percent * barLength) / 100; bar := ""
	for i := 0; i < barLength; i++ { if i < filledLength { bar += "█" } else { bar += "░" } }
	term.Write([]byte(fmt.Sprintf("\r%s %s [%s] %d%%", colorCode, label, bar, percent)))
}

func CheckRoot() bool {
	cmd := exec.Command("su", "-c", "id -u")
	out, err := cmd.Output()
	if err != nil { return false }
	return strings.TrimSpace(string(out)) == "0"
}

// Cek Driver via Kallsyms
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

func RequestStoragePermission(term *Terminal) {
	if term != nil { term.Write([]byte("\x1b[33m[*] Opening Settings: All Files Access...\x1b[0m\n")) }
	pkgName := "com.tangsan.executor"
	cmd1 := exec.Command("sh", "-c", fmt.Sprintf("am start -a android.settings.MANAGE_APP_ALL_FILES_ACCESS_PERMISSION -d package:%s", pkgName))
	if cmd1.Run() != nil {
		if term != nil { term.Write([]byte("\x1b[33m[!] Trying generic settings...\x1b[0m\n")) }
		exec.Command("sh", "-c", "am start -a android.settings.MANAGE_ALL_FILES_ACCESS_PERMISSION").Run()
	}
}

func downloadFile(url string, filepath string) (error, string) {
	exec.Command("su", "-c", "rm -f "+filepath).Run()
	cmdStr := fmt.Sprintf("curl -k -L -f --connect-timeout 10 -o %s %s", filepath, url)
	cmd := exec.Command("su", "-c", cmdStr)
	err := cmd.Run()
	if err == nil {
		checkCmd := exec.Command("su", "-c", "[ -s "+filepath+" ]")
		if checkCmd.Run() == nil { return nil, "Success" }
	}
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil { return err, "Init Fail" }
	req.Header.Set("User-Agent", "Mozilla/5.0 Chrome/120.0.0.0")
	resp, err := client.Do(req)
	if err != nil { return err, "Net Err" }
	defer resp.Body.Close()
	if resp.StatusCode != 200 { return fmt.Errorf("HTTP %d", resp.StatusCode), "HTTP Err" }
	writeCmd := exec.Command("su", "-c", "cat > "+filepath)
	stdin, err := writeCmd.StdinPipe()
	if err != nil { return err, "Pipe Err" }
	go func() { defer stdin.Close(); io.Copy(stdin, resp.Body) }()
	if err := writeCmd.Run(); err != nil { return err, "Write Err" }
	return nil, "Success"
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

	w := a.NewWindow("Simple Exec by TANGSAN")
	w.Resize(fyne.NewSize(400, 700))
	w.SetMaster()

	term := NewTerminal()
	
	go func() {
		time.Sleep(1 * time.Second)
		if !CheckRoot() { /* Optional Auto Request */ }
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
				
				// Panggil Deteksi Kallsyms
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
		status.Text = "Status: Processing..."
		status.Refresh()

		if !isScript {
			displayDir := currentDir
			if len(displayDir) > 25 { displayDir = "..." + displayDir[len(displayDir)-20:] }
			term.Write([]byte(fmt.Sprintf("\x1b[33m%s \x1b[36m> \x1b[0m%s\n", displayDir, cmdText)))
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

			cmd.Env = append(os.Environ(), "TERM=xterm-256color")
			stdin, _ := cmd.StdinPipe()
			stdout, _ := cmd.StdoutPipe()
			stderr, _ := cmd.StderrPipe()
			cmdMutex.Lock(); activeStdin = stdin; cmdMutex.Unlock()

			if err := cmd.Start(); err != nil {
				term.Write([]byte(fmt.Sprintf("\x1b[31mError: %s\x1b[0m\n", err.Error())))
				cmdMutex.Lock(); activeStdin = nil; cmdMutex.Unlock()
				return
			}
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); io.Copy(term, stdout) }()
			go func() { defer wg.Done(); io.Copy(term, stderr) }()
			wg.Wait(); cmd.Wait()
			cmdMutex.Lock(); activeStdin = nil; cmdMutex.Unlock()
			status.Text = "Status: Idle"; status.Refresh()
			if isScript && isRoot { exec.Command("su", "-c", "rm -f /data/local/tmp/temp_exec").Run() }
		}()
	}

	send := func() {
		text := input.Text
		input.SetText("")
		cmdMutex.Lock()
		if activeStdin != nil { io.WriteString(activeStdin, text+"\n"); term.Write([]byte(text + "\n")); cmdMutex.Unlock(); return }
		cmdMutex.Unlock()
		if strings.TrimSpace(text) == "" { return }
		
		if strings.HasPrefix(text, "cd") {
			parts := strings.Fields(text)
			newPath := currentDir
			if len(parts) == 1 { if CheckRoot() { newPath = "/data/local/tmp" } else { h, _ := os.UserHomeDir(); newPath = h } } else { arg := parts[1]; if filepath.IsAbs(arg) { newPath = arg } else { newPath = filepath.Join(currentDir, arg) } }
			newPath = filepath.Clean(newPath)
			exist := false
			if CheckRoot() { if exec.Command("su", "-c", "[ -d \""+newPath+"\" ]").Run() == nil { exist = true } } else { if info, err := os.Stat(newPath); err == nil && info.IsDir() { exist = true } }
			if exist { currentDir = newPath; displayDir := currentDir; if len(displayDir) > 25 { displayDir = "..." + displayDir[len(displayDir)-20:] }; term.Write([]byte(fmt.Sprintf("\x1b[33m%s \x1b[36m> \x1b[0mcd %s\n", displayDir, parts[1]))) } else { term.Write([]byte(fmt.Sprintf("\x1b[31mcd: %s: No such directory\x1b[0m\n", parts[1]))) }
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
	
	// FUNGSI POPUP (UPDATED: LOGIKA WARNA TOMBOL RETRY)
	showModal := func(title, msg, confirm string, action func(), isErr bool, isForce bool) {
		w.Canvas().Refresh(w.Content())
		
		cancelLabel := "BATAL"
		cancelFunc := func() { overlayContainer.Hide() }
		
		if isForce {
			cancelLabel = "KELUAR"
			cancelFunc = func() { os.Exit(0) }
		}
		
		btnCancel := widget.NewButton(cancelLabel, cancelFunc)
		btnCancel.Importance = widget.DangerImportance
		
		btnOk := widget.NewButton(confirm, func() {
			if !isForce { overlayContainer.Hide() }
			if action != nil { action() }
		})
		
		// LOGIKA WARNA TOMBOL RETRY
		if confirm == "COBA LAGI" {
			btnOk.Importance = widget.HighImportance
		} else {
			if isErr { 
				btnOk.Importance = widget.DangerImportance 
			} else { 
				btnOk.Importance = widget.HighImportance 
			}
		}
		
		btnBox := container.NewHBox(
			layout.NewSpacer(), 
			container.NewGridWrap(fyne.NewSize(110,40), btnCancel), 
			widget.NewLabel("   "), 
			container.NewGridWrap(fyne.NewSize(110,40), btnOk), 
			layout.NewSpacer(),
		)
		
		lblTitle := createLabel(title, theme.ForegroundColor(), 18, true)
		if isErr { lblTitle.Color = theme.ErrorColor() }
		
		lblMsg := widget.NewLabel(msg)
		lblMsg.Alignment = fyne.TextAlignCenter 
		lblMsg.Wrapping = fyne.TextWrapWord
		
		content := container.NewVBox(
			container.NewPadded(container.NewCenter(lblTitle)), 
			container.NewPadded(lblMsg), 
			widget.NewLabel(""), 
			btnBox,
		)
		
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

			// 1. Cek Versi Kernel
			out, _ := exec.Command("uname", "-r").Output()
			fullVer := strings.TrimSpace(string(out))
			targetVer := strings.Split(fullVer, "-")[0]
			
			term.Write([]byte(fmt.Sprintf("Kernel: \x1b[33m%s\x1b[0m\n", fullVer)))

			// Path tujuan
			targetKoPath := "/data/local/tmp/module_inject.ko"
			
			// DEFER cleanup
			defer func() {
				exec.Command("su", "-c", "rm -f "+targetKoPath).Run()
			}()

			// 2. Baca ZIP Embed
			term.Write([]byte("\x1b[97m[*] Membaca file driver internal...\x1b[0m\n"))
			zipReader, err := zip.NewReader(bytes.NewReader(driverZip), int64(len(driverZip)))
			if err != nil {
				term.Write([]byte("\x1b[31m[ERR] File Zip Rusak/Corrupt\x1b[0m\n")); return
			}

			// 3. Cari File .ko
			var fileToExtract *zip.File
			for _, f := range zipReader.File {
				if strings.HasSuffix(f.Name, ".ko") && strings.Contains(f.Name, targetVer) {
					fileToExtract = f; break
				}
			}
			
			// Fallback
			if fileToExtract == nil {
				for _, f := range zipReader.File {
					if strings.HasSuffix(f.Name, ".ko") { fileToExtract = f; break }
				}
			}

			if fileToExtract == nil {
				term.Write([]byte("\x1b[31m[GAGAL] Modul .ko tidak ditemukan di dalam Zip!\x1b[0m\n"))
				status.Text = "File Hilang"; status.Refresh()
				return
			}

			// 4. Ekstrak
			term.Write([]byte(fmt.Sprintf("\x1b[32m[+] Menggunakan File: %s\x1b[0m\n", fileToExtract.Name)))
			rc, _ := fileToExtract.Open()
			buf := new(bytes.Buffer); io.Copy(buf, rc); rc.Close()
			
			userTmp := filepath.Join(os.TempDir(), "temp_mod.ko")
			os.WriteFile(userTmp, buf.Bytes(), 0644)
			
			// Pindah ke root path
			exec.Command("su", "-c", fmt.Sprintf("cp %s %s", userTmp, targetKoPath)).Run()
			exec.Command("su", "-c", "chmod 777 "+targetKoPath).Run()
			os.Remove(userTmp) 

			// 5. Install (Insmod)
			term.Write([]byte("\x1b[36m[*] Memasang Modul (Inject)...\x1b[0m\n"))
			cmdInsmod := exec.Command("su", "-c", "insmod "+targetKoPath)
			output, err := cmdInsmod.CombinedOutput()
			outputStr := string(output)

			// 6. Cek Hasil
			if err == nil {
				term.Write([]byte("\x1b[92m[SUKSES] Driver Berhasil Di install\x1b[0m\n"))
				lblKernelValue.Text = "ACTIVE"; lblKernelValue.Color = successGreen
				status.Text = "Berhasil Install"
			} else if strings.Contains(outputStr, "File exists") {
				term.Write([]byte("\x1b[33m[INFO] Driver Sudah Ada Ketik insmod untuk cek lebih lanjut\x1b[0m\n"))
				lblKernelValue.Text = "ACTIVE"; lblKernelValue.Color = successGreen
				status.Text = "Sudah Aktif"
			} else {
				term.Write([]byte("\x1b[31m[GAGAL] Gagal install\x1b[0m\n"))
				term.Write([]byte("\x1b[31m" + outputStr + "\x1b[0m\n"))
				lblKernelValue.Text = "ERROR"; lblKernelValue.Color = failRed
				status.Text = "Gagal Install"
			}
			
			lblKernelValue.Refresh()
			status.Refresh()
		}()
	}

	// DEFINISI FUNGSI UPDATE (RETRY LOGIC)
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
						showModal("UPDATE", cfg.Message, "UPDATE", func() { 
							if u, e := url.Parse(cfg.Link); e == nil { app.New().OpenURL(u) } 
						}, false, true)
					} else {
						term.Write([]byte("\x1b[32m[V] System Updated.\x1b[0m\n"))
					}
				}
			}
		} else {
			// POPUP RETRY
			showModal("ERROR", "Gagal terhubung ke server.\nPeriksa koneksi internet.", "COBA LAGI", func() {
				go checkUpdate()
			}, true, true)
		}
	}

	// JALANKAN CHECK UPDATE PERTAMA KALI
	go func() {
		time.Sleep(1500 * time.Millisecond)
		checkUpdate()
	}()

	titleText := createLabel("SIMPLE EXECUTOR", color.White, 16, true)
	
	infoGrid := container.NewGridWithColumns(3, 
		container.NewVBox(lblKernelTitle, lblKernelValue), 
		container.NewVBox(lblSELinuxTitle, lblSELinuxValue), 
		container.NewVBox(lblSystemTitle, lblSystemValue),
	)

	btnInj := widget.NewButtonWithIcon("Inject", theme.DownloadIcon(), func() { 
		showModal("INJECT", "Mulai Inject Driver?", "MULAI", autoInstallKernel, false, false) 
	})
	btnInj.Importance = widget.HighImportance

	btnSel := widget.NewButtonWithIcon("SELinux", theme.ViewRefreshIcon(), func() { 
		go func() { 
			if CheckSELinux() == "Enforcing" { 
				exec.Command("su","-c","setenforce 0").Run() 
			} else { 
				exec.Command("su","-c","setenforce 1").Run() 
			}
			time.Sleep(100 * time.Millisecond)
			se := CheckSELinux()
			lblSELinuxValue.Text = strings.ToUpper(se)
			if se == "Enforcing" { 
				lblSELinuxValue.Color = successGreen 
			} else if se == "Permissive" { 
				lblSELinuxValue.Color = failRed 
			} else { 
				lblSELinuxValue.Color = color.Gray{Y: 150} 
			}
			lblSELinuxValue.Refresh()
		}() 
	})
	btnSel.Importance = widget.HighImportance

	btnClr := widget.NewButtonWithIcon("Clear", theme.ContentClearIcon(), func() { 
		term.Clear() 
	})
	btnClr.Importance = widget.DangerImportance
	
	headerContent := container.NewVBox(
		container.NewPadded(titleText), 
		container.NewPadded(infoGrid), 
		container.NewPadded(container.NewGridWithColumns(3, btnInj, btnSel, btnClr)), 
		container.NewPadded(status), 
		widget.NewSeparator(),
	)
	header := container.NewStack(canvas.NewRectangle(color.Gray{Y: 45}), headerContent)

	sendBtn := widget.NewButtonWithIcon("", theme.MailSendIcon(), send)
	cpyLbl := createLabel("Code by TANGSAN", silverColor, 10, false)
	bottom := container.NewVBox(container.NewPadded(cpyLbl), container.NewPadded(container.NewBorder(nil, nil, nil, sendBtn, input)))
	bg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "bg.png", StaticContent: bgPng}); bg.FillMode = canvas.ImageFillStretch
	termBox := container.NewStack(canvas.NewRectangle(color.Black), bg, canvas.NewRectangle(color.RGBA{0,0,0,180}), term.scroll)
	
	fdImg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "fd.png", StaticContent: fdPng})
	fdImg.FillMode = canvas.ImageFillContain
	
	fdBtn := widget.NewButton("", func() { 
		dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) { 
			if r != nil { runFile(r) } 
		}, w).Show() 
	})
	fdBtn.Importance = widget.LowImportance
	
	fabWrapper := container.NewGridWrap(fyne.NewSize(65,65), container.NewStack(container.NewPadded(fdImg), fdBtn))
	fab := container.NewVBox(
		layout.NewSpacer(), 
		container.NewPadded(container.NewHBox(layout.NewSpacer(), fabWrapper)), 
		widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "),
	)

	overlayContainer = container.NewStack(); overlayContainer.Hide()
	w.SetContent(container.NewStack(container.NewBorder(header, bottom, nil, nil, termBox), fab, overlayContainer))
	w.ShowAndRun()
}

