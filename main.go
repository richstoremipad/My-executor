package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json" // Digunakan untuk JSON unmarshal
	"errors"
	"fmt"
	"image/color"
	"io"
	"net/http"
	"net/url" // Digunakan untuk parsing URL update
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
   CONFIG & UPDATE SYSTEM
========================================== */
const AppVersion = "1.0"
const ConfigURL = "https://raw.githubusercontent.com/tangsanrich/Fileku/main/executor.txt"
const CryptoKey = "RahasiaNegaraJanganSampaiBocorir"
const MaxScrollback = 100 

// Game Configs
const AccountFile = "/sdcard/akun.ini"
const OnlineAccFile = "/sdcard/accml_online.ini"
const UrlConfigFile = "/sdcard/ml_url_config.ini"

var PackageNames = []string{"com.mobile.legends", "com.hhgame.mlbbvn", "com.mobile.legends.usa", "com.mobile.v2l"}
var AppNames     = []string{"🌍 Global", "🇻🇳 VNG", "🇺🇸 USA", "🇮🇳 M6"}
var SelectedGameIdx = 0 // Default Global

var currentDir string = "/sdcard" 
var activeStdin io.WriteCloser
var cmdMutex sync.Mutex

// Struct ini menggunakan encoding/json
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
   MLBB LOGIC INTEGRATION
========================================== */
func cleanGhostSpaces(s string) string {
	s = strings.ReplaceAll(s, "\ufeff", "")
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

func generateRandomID() string {
	randHex := func(n int) string {
		bytes := make([]byte, n/2)
		if _, err := rand.Read(bytes); err != nil { return "" }
		return hex.EncodeToString(bytes)
	}
	return fmt.Sprintf("%s-%s-%s-%s-%s", randHex(56), randHex(4), randHex(4), randHex(4), randHex(12))
}

// Fungsi core untuk inject ID
func applyDeviceIDLogic(term *Terminal, targetID, targetPkg, targetAppName, customAccName string) bool {
	targetID = cleanGhostSpaces(targetID)
	targetFile := fmt.Sprintf("/data/user/0/%s/shared_prefs/%s.v2.playerprefs.xml", targetPkg, targetPkg)

	term.Write([]byte(fmt.Sprintf("\x1b[35m[PROCESS] Target: %s (%s)\x1b[0m\n", targetAppName, targetPkg)))
	if customAccName != "" {
		term.Write([]byte(fmt.Sprintf("\x1b[36m[INFO] Akun: %s\x1b[0m\n", customAccName)))
	} else {
		term.Write([]byte(fmt.Sprintf("\x1b[36m[INFO] ID Baru: %s\x1b[0m\n", targetID)))
	}

	// Cek File
	if exec.Command("su", "-c", fmt.Sprintf("[ -f '%s' ]", targetFile)).Run() != nil {
		term.Write([]byte("\x1b[31m[ERROR] File data game belum ada! Login dulu sekali.\x1b[0m\n"))
		return false
	}

	cmds := []string{
		fmt.Sprintf("am force-stop %s", targetPkg),
		fmt.Sprintf("rm -rf /data/user/0/%s/shared_prefs/__gpm__.xml", targetPkg),
		fmt.Sprintf("sed -i 's/.*<string name=\"JsonDeviceID\">.*/    <string name=\"JsonDeviceID\">%s<\\/string>/' %s", targetID, targetFile),
		fmt.Sprintf("sed -i 's/.*<string name=\"__Java_JsonDeviceID__\">.*/    <string name=\"__Java_JsonDeviceID\">%s<\\/string>/' %s", targetID, targetFile),
	}

	for _, cmd := range cmds {
		term.Write([]byte(fmt.Sprintf("\x1b[90m> %s\x1b[0m\n", cmd))) // Log verbose
		exec.Command("su", "-c", cmd).Run()
	}

	term.Write([]byte("\x1b[32m[SUCCESS] Device ID Berhasil diterapkan.\x1b[0m\n"))
	return true
}

/* ==========================================
   TERMINAL UI LOGIC
========================================== */
type Terminal struct {
	grid         *widget.TextGrid
	scroll       *container.Scroll
	curRow       int
	curCol       int
	curStyle     *widget.CustomTextGridStyle
	mutex        sync.Mutex
	reAnsi       *regexp.Regexp
	needsRefresh bool
}

func NewTerminal() *Terminal {
	g := widget.NewTextGrid()
	g.ShowLineNumbers = false
	defStyle := &widget.CustomTextGridStyle{
		FGColor: theme.ForegroundColor(),
		BGColor: color.Transparent,
	}
	re := regexp.MustCompile(`\x1b\[([0-9;]*)?([a-zA-Z])`)
	term := &Terminal{
		grid:         g,
		scroll:       container.NewScroll(g),
		curRow:       0,
		curCol:       0,
		curStyle:     defStyle,
		reAnsi:       re,
		needsRefresh: false,
	}
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

func downloadFile(url string, filepath string) error {
	resp, err := http.Get(url)
	if err != nil { return err }
	defer resp.Body.Close()
	out, err := os.Create(filepath)
	if err != nil { return err }
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

/* ==========================================
   UI NAVIGATION LOGIC
========================================== */

// EdgeTrigger mendeteksi geseran dari sisi kiri layar
type EdgeTrigger struct {
	widget.BaseWidget
	OnOpen func()
}

func NewEdgeTrigger(onOpen func()) *EdgeTrigger {
	e := &EdgeTrigger{OnOpen: onOpen}
	e.ExtendBaseWidget(e)
	return e
}

func (e *EdgeTrigger) Dragged(event *fyne.DragEvent) {
	if event.Dragged.DX > 5 {
		if e.OnOpen != nil { e.OnOpen() }
	}
}

func (e *EdgeTrigger) DragEnd() {}

func (e *EdgeTrigger) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(canvas.NewRectangle(color.Transparent))
}

// VIEW MANAGER
var currentView = "TERMINAL" // TERMINAL or GAMETOOLS
// Perbaikan Tipe Data: *container.Stack diubah menjadi *fyne.Container
var contentContainer *fyne.Container 
var terminalView *fyne.Container
var gameToolsView *fyne.Container

func switchView(viewName string) {
	currentView = viewName
	contentContainer.Objects = nil
	if viewName == "TERMINAL" {
		contentContainer.Add(terminalView)
	} else if viewName == "GAMETOOLS" {
		contentContainer.Add(gameToolsView)
	}
	contentContainer.Refresh()
}

/* ===============================
              MAIN UI
================================ */
func main() {
	a := app.New()
	a.Settings().SetTheme(theme.DarkTheme())

	w := a.NewWindow("Executor Pro")
	w.Resize(fyne.NewSize(400, 750))
	w.SetMaster()

	term := NewTerminal()
	
	// INIT ROOT & DIRS
	go func() {
		time.Sleep(1 * time.Second)
		if !CheckRoot() { /* auto request */ }
	}()
	if !CheckRoot() { currentDir = "/sdcard" }

	/* ---------------------------
	   UPDATE CHECKER LOGIC (RE-ADDED TO USE IMPORTS)
	   --------------------------- */
	// Fungsi ini memastikan encoding/json dan net/url terpakai
	go func() {
		time.Sleep(2 * time.Second)
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(fmt.Sprintf("%s?v=%d", ConfigURL, time.Now().Unix()))
		
		if err == nil && resp.StatusCode == 200 {
			body, _ := io.ReadAll(resp.Body); resp.Body.Close()
			if dec, err := decryptConfig(string(bytes.TrimSpace(body))); err == nil {
				var cfg OnlineConfig
				// Menggunakan encoding/json
				if json.Unmarshal(dec, &cfg) == nil {
					if cfg.Version != "" && cfg.Version != AppVersion {
						// Menggunakan net/url
						if u, e := url.Parse(cfg.Link); e == nil {
							term.Write([]byte(fmt.Sprintf("\x1b[33m[UPDATE] Versi baru tersedia: %s\x1b[0m\n", cfg.Version)))
							dialog.ShowConfirm("Update", cfg.Message, func(b bool) {
								if b { a.OpenURL(u) }
							}, w)
						}
					}
				}
			}
		}
	}()

	/* ---------------------------
	   UI COMPONENTS - TERMINAL
	   --------------------------- */
	input := widget.NewEntry()
	input.SetPlaceHolder("Command...")
	
	status := canvas.NewText("System: Ready", color.Gray{Y: 180})
	status.TextSize = 12; status.Alignment = fyne.TextAlignCenter

	// Status Grid (Kernel, SELinux, Root)
	brightYellow := color.RGBA{R: 255, G: 255, B: 0, A: 255}
	successGreen := color.RGBA{R: 0, G: 255, B: 0, A: 255}
	failRed := color.RGBA{R: 255, G: 50, B: 50, A: 255}

	lblKernelValue := createLabel("...", color.Gray{Y: 150}, 11, true)
	lblSELinuxValue := createLabel("...", color.Gray{Y: 150}, 11, true)
	lblSystemValue := createLabel("...", color.Gray{Y: 150}, 11, true)

	// Monitor System loop
	go func() {
		for {
			func() {
				defer func() { if r := recover(); r != nil {} }()
				if CheckRoot() { lblSystemValue.Text = "GRANTED"; lblSystemValue.Color = successGreen } else { lblSystemValue.Text = "DENIED"; lblSystemValue.Color = failRed }
				if CheckKernelDriver() { lblKernelValue.Text = "ACTIVE"; lblKernelValue.Color = successGreen } else { lblKernelValue.Text = "MISSING"; lblKernelValue.Color = failRed }
				se := CheckSELinux()
				lblSELinuxValue.Text = strings.ToUpper(se)
				if se == "Enforcing" { lblSELinuxValue.Color = successGreen } else { lblSELinuxValue.Color = failRed }
				
				lblSystemValue.Refresh(); lblKernelValue.Refresh(); lblSELinuxValue.Refresh()
			}()
			time.Sleep(3 * time.Second)
		}
	}()

	// Execute Logic
	executeTask := func(cmdText string, isScript bool, scriptPath string, isBinary bool) {
		status.Text = "Status: Busy..."
		status.Refresh()
		if !isScript {
			displayDir := currentDir
			if len(displayDir) > 20 { displayDir = "..." + displayDir[len(displayDir)-17:] }
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
					// Local shell fallback logic
					runCmd := cmdText
					if strings.HasPrefix(cmdText, "ls") && !strings.Contains(cmdText, "-a") { runCmd = strings.Replace(cmdText, "ls", "ls -a", 1) }
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
				cmdMutex.Lock(); activeStdin = nil; cmdMutex.Unlock(); return
			}
			var wg sync.WaitGroup; wg.Add(2)
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
			// CD Logic simplified
			parts := strings.Fields(text)
			newPath := currentDir
			if len(parts) > 1 { newPath = filepath.Join(currentDir, parts[1]) }
			currentDir = newPath // Basic naive impl for UI
			term.Write([]byte(fmt.Sprintf("\x1b[33m%s \x1b[36m> \x1b[0mcd %s\n", currentDir, parts[1])))
			return
		}
		executeTask(text, false, "", false)
	}
	input.OnSubmitted = func(_ string) { send() }

	// Header Widgets
	infoGrid := container.NewGridWithColumns(3, 
		container.NewVBox(createLabel("KERNEL", brightYellow, 10, true), lblKernelValue), 
		container.NewVBox(createLabel("SELINUX", brightYellow, 10, true), lblSELinuxValue), 
		container.NewVBox(createLabel("ROOT", brightYellow, 10, true), lblSystemValue),
	)
	
	// Inject Logic
	autoInstallKernel := func() {
		term.Clear()
		term.Write([]byte("\x1b[36m[*] Memulai Inject Driver...\x1b[0m\n"))
		go func() {
			out, _ := exec.Command("uname", "-r").Output()
			ver := strings.TrimSpace(string(out))
			targetVer := strings.Split(ver, "-")[0]
			term.Write([]byte(fmt.Sprintf("Kernel: %s\n", ver)))
			
			targetKo := "/data/local/tmp/module_inject.ko"
			exec.Command("su", "-c", "rm -f "+targetKo).Run()
			
			zr, err := zip.NewReader(bytes.NewReader(driverZip), int64(len(driverZip)))
			if err != nil { term.Write([]byte("\x1b[31m[ERR] Zip Corrupt\x1b[0m\n")); return }
			
			var f *zip.File
			for _, zf := range zr.File { if strings.Contains(zf.Name, targetVer) && strings.HasSuffix(zf.Name, ".ko") { f = zf; break } }
			if f == nil { for _, zf := range zr.File { if strings.HasSuffix(zf.Name, ".ko") { f = zf; break } } } // fallback
			
			if f != nil {
				rc, _ := f.Open()
				buf := new(bytes.Buffer); io.Copy(buf, rc); rc.Close()
				os.WriteFile(os.TempDir()+"/tmp.ko", buf.Bytes(), 0644)
				exec.Command("su", "-c", fmt.Sprintf("cp %s %s && chmod 777 %s", os.TempDir()+"/tmp.ko", targetKo, targetKo)).Run()
				if exec.Command("su", "-c", "insmod "+targetKo).Run() == nil {
					term.Write([]byte("\x1b[32m[OK] Driver Loaded.\x1b[0m\n"))
				} else { term.Write([]byte("\x1b[31m[FAIL] Insmod Failed/Exists.\x1b[0m\n")) }
			} else { term.Write([]byte("\x1b[31m[ERR] Driver not found in zip.\x1b[0m\n")) }
		}()
	}

	btnInj := widget.NewButton("Inject Driver", autoInstallKernel)
	btnInj.Importance = widget.HighImportance

	/* ---------------------------
	   UI COMPONENTS - GAME TOOLS (NEW)
	   --------------------------- */
	
	// Select Target Game
	selectGame := widget.NewSelect(AppNames, func(s string) {
		for i, v := range AppNames { if v == s { SelectedGameIdx = i } }
	})
	selectGame.SetSelected(AppNames[0])

	// Feature: Reset ID
	btnResetID := widget.NewButtonWithIcon("Reset ID (Random)", theme.ViewRefreshIcon(), func() {
		dialog.ShowConfirm("Reset ID", "Yakin ingin ganti ID Baru?", func(b bool) {
			if b {
				newID := generateRandomID()
				applyDeviceIDLogic(term, newID, PackageNames[SelectedGameIdx], AppNames[SelectedGameIdx], "")
				switchView("TERMINAL") // Switch back to see logs
			}
		}, w)
	})

	// Feature: Copy ID
	btnCopyID := widget.NewButtonWithIcon("Salin ID dari Game Lain", theme.ContentCopyIcon(), func() {
		// Custom Dialog for Source Selection
		selSrc := widget.NewSelect(AppNames, nil)
		selSrc.PlaceHolder = "Pilih Sumber"
		dialog.ShowCustomConfirm("Salin ID", "Salin", "Batal", container.NewVBox(widget.NewLabel("Salin ID Dari:"), selSrc), func(b bool) {
			if b && selSrc.Selected != "" {
				srcIdx := 0
				for i, v := range AppNames { if v == selSrc.Selected { srcIdx = i } }
				
				// Get ID via shell
				cmdStr := fmt.Sprintf("sed -n 's/.*<string name=\"JsonDeviceID\">\\([^<]*\\)<.*/\\1/p' /data/user/0/%s/shared_prefs/%s.v2.playerprefs.xml | head -n 1", PackageNames[srcIdx], PackageNames[srcIdx])
				out, err := exec.Command("su", "-c", cmdStr).Output()
				srcID := strings.TrimSpace(string(out))
				
				if err == nil && len(srcID) > 5 {
					dialog.ShowConfirm("Konfirmasi", fmt.Sprintf("ID Ditemukan: %s\nTempel ke %s?", srcID, AppNames[SelectedGameIdx]), func(yes bool) {
						if yes {
							applyDeviceIDLogic(term, srcID, PackageNames[SelectedGameIdx], AppNames[SelectedGameIdx], "")
							switchView("TERMINAL")
						}
					}, w)
				} else {
					dialog.ShowError(errors.New("Gagal mengambil ID Sumber"), w)
				}
			}
		}, w)
	})

	// Feature: Login Account (Complex)
	btnLogin := widget.NewButtonWithIcon("Login Akun", theme.AccountIcon(), func() {
		// Step 1: Choose Source
		dialog.ShowCustomConfirm("Pilih Sumber Akun", "Online (Cloud)", "Offline (Lokal)", widget.NewLabel("Dari mana ambil data akun?"), func(isOnline bool) {
			
			processFile := func(filePath string) {
				// Parse File
				f, err := os.Open(filePath)
				if err != nil { dialog.ShowError(errors.New("File tidak ditemukan"), w); return }
				defer f.Close()
				
				var accList []string
				var rawIDs []string
				var rawNames []string
				
				scanner := bufio.NewScanner(f)
				for scanner.Scan() {
					line := strings.TrimSpace(scanner.Text())
					if line != "" && !strings.HasPrefix(line, "#") {
						parts := strings.Fields(line)
						if len(parts) > 0 {
							rawIDs = append(rawIDs, parts[0])
							nm := "No Name"
							if len(parts) > 1 { nm = strings.Join(parts[1:], " ") }
							rawNames = append(rawNames, nm)
							accList = append(accList, nm)
						}
					}
				}

				if len(accList) == 0 { dialog.ShowError(errors.New("Tidak ada akun di file"), w); return }

				// Step 2: List Account
				list := widget.NewList(
					func() int { return len(accList) },
					func() fyne.CanvasObject { return widget.NewLabel("template") },
					func(i int, o fyne.CanvasObject) { o.(*widget.Label).SetText(accList[i]) },
				)
				
				var d dialog.Dialog
				list.OnSelected = func(id int) {
					d.Hide()
					// Step 3: Execute
					applyDeviceIDLogic(term, rawIDs[id], PackageNames[SelectedGameIdx], AppNames[SelectedGameIdx], rawNames[id])
					switchView("TERMINAL")
					
					// Auto Launch
					exec.Command("su", "-c", fmt.Sprintf("am start -n %s/com.moba.unityplugin.MobaGameUnityActivity", PackageNames[SelectedGameIdx])).Run()
					if isOnline { os.Remove(OnlineAccFile) } // Cleanup
				}

				cont := container.NewStack(list)
				d = dialog.NewCustom("Pilih Akun", "Batal", container.NewGridWrap(fyne.NewSize(300, 400), cont), w)
				d.Show()
			}

			if isOnline {
				// Online Logic
				entryUrl := widget.NewEntry()
				entryUrl.PlaceHolder = "https://..."
				// Auto load saved url
				if b, e := os.ReadFile(UrlConfigFile); e == nil { entryUrl.SetText(string(b)) }
				
				dialog.ShowCustomConfirm("Download Akun", "Download", "Batal", entryUrl, func(dl bool) {
					if dl && entryUrl.Text != "" {
						os.WriteFile(UrlConfigFile, []byte(entryUrl.Text), 0644)
						prog := dialog.NewProgressInfinite("Downloading...", "Mohon tunggu", w)
						prog.Show()
						go func() {
							err := downloadFile(entryUrl.Text, OnlineAccFile)
							prog.Hide()
							if err != nil { dialog.ShowError(err, w); return }
							processFile(OnlineAccFile)
						}()
					}
				}, w)
			} else {
				// Offline Logic
				processFile(AccountFile)
			}
		}, w)
	})
	btnLogin.Importance = widget.HighImportance

	// Feature: SELinux Switch
	btnSELinux := widget.NewButton("Switch SELinux", func() {
		if CheckSELinux() == "Enforcing" {
			exec.Command("su", "-c", "setenforce 0").Run()
		} else {
			exec.Command("su", "-c", "setenforce 1").Run()
		}
		switchView("TERMINAL") // To refresh header status
	})

	// Layout Game Tools
	toolsContent := container.NewVBox(
		widget.NewCard("Target Game", "Pilih Versi MLBB", selectGame),
		widget.NewSeparator(),
		widget.NewLabel("Fitur Akun"),
		container.NewGridWithColumns(2, btnLogin, btnResetID),
		btnCopyID,
		widget.NewSeparator(),
		widget.NewLabel("System"),
		btnSELinux,
		widget.NewLabel(""),
		widget.NewCard("Info", "", widget.NewLabel("Pastikan Root aktif sebelum menggunakan fitur ini.")),
	)
	
	// Create Scrollable Views
	bg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "bg.png", StaticContent: bgPng})
	bg.FillMode = canvas.ImageFillStretch

	// 1. TERMINAL VIEW
	sendBtn := widget.NewButtonWithIcon("", theme.MailSendIcon(), send)
	bottomTerm := container.NewBorder(nil, nil, nil, sendBtn, input)
	
	// Header Section (Always Visible in Terminal)
	headerTerm := container.NewVBox(
		createLabel("TERMINAL EXECUTOR", color.White, 14, true),
		container.NewPadded(infoGrid),
		container.NewPadded(btnInj),
		status,
		widget.NewSeparator(),
	)

	terminalView = container.NewStack(
		canvas.NewRectangle(color.Black), 
		bg, 
		canvas.NewRectangle(color.RGBA{0,0,0,200}),
		container.NewBorder(headerTerm, bottomTerm, nil, nil, term.scroll),
	)

	// 2. GAMETOOLS VIEW
	headerGame := container.NewVBox(
		createLabel("MLBB TOOLS", theme.PrimaryColor(), 18, true),
		createLabel("By Tangsan", color.Gray{Y:150}, 10, false),
		widget.NewSeparator(),
	)
	
	gameToolsView = container.NewStack(
		canvas.NewRectangle(color.Black),
		bg,
		canvas.NewRectangle(color.RGBA{0,0,0,220}), // Darker bg for tools
		container.NewBorder(headerGame, nil, nil, nil, container.NewPadded(container.NewVScroll(toolsContent))),
	)

	// MAIN CONTAINER STACK
	contentContainer = container.NewStack(terminalView) // Default Terminal

	/* ---------------------------
	   SIDE MENU LOGIC
	   --------------------------- */
	var toggleMenu func()
	
	// Menu Content
	menuBg := canvas.NewRectangle(theme.BackgroundColor())
	lblMenu := createLabel("MENU UTAMA", theme.PrimaryColor(), 16, true)
	
	btnMenuTerm := widget.NewButtonWithIcon("Terminal", theme.ComputerIcon(), func() {
		switchView("TERMINAL")
		toggleMenu()
	})
	btnMenuTerm.Alignment = widget.ButtonAlignLeading
	
	btnMenuGame := widget.NewButtonWithIcon("Game Tools", theme.GridIcon(), func() {
		switchView("GAMETOOLS")
		toggleMenu()
	})
	btnMenuGame.Alignment = widget.ButtonAlignLeading

	btnMenuInfo := widget.NewButtonWithIcon("Tentang", theme.InfoIcon(), func() {
		dialog.ShowInformation("About", "Executor Pro v"+AppVersion+"\nGUI Version of MLBB Script\nCreated by Tangsan", w)
		toggleMenu()
	})

	menuItems := container.NewVBox(
		container.NewPadded(lblMenu),
		widget.NewSeparator(),
		btnMenuTerm,
		btnMenuGame,
		btnMenuInfo,
		layout.NewSpacer(),
		widget.NewButtonWithIcon("Keluar", theme.LogoutIcon(), func(){ os.Exit(0) }),
	)
	
	sidePanel := container.NewStack(menuBg, container.NewPadded(menuItems))
	sideContainer := container.NewHBox(container.NewGridWrap(fyne.NewSize(260, 800), sidePanel))
	
	// Dimmer (Click to close menu)
	dimmer := canvas.NewRectangle(color.RGBA{0,0,0,150})
	btnDimmer := widget.NewButton("", func(){ toggleMenu() })
	btnDimmer.Importance = widget.LowImportance
	menuStack := container.NewStack(container.NewStack(dimmer, btnDimmer), sideContainer)
	menuStack.Hide()

	toggleMenu = func() {
		if menuStack.Visible() { menuStack.Hide() } else { menuStack.Show(); menuStack.Refresh() }
	}

	// Gesture Trigger
	edgeTrigger := NewEdgeTrigger(func() { if !menuStack.Visible() { toggleMenu() } })
	triggerZone := container.NewHBox(container.NewGridWrap(fyne.NewSize(20, 1000), edgeTrigger), layout.NewSpacer())

	/* ---------------------------
	   FINAL ASSEMBLY
	   --------------------------- */
	
	// FAB (File Open) - Only show in Terminal View ideally, but kept global for simplicity
	fdImg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "fd.png", StaticContent: fdPng})
	fdImg.FillMode = canvas.ImageFillContain
	fdBtn := widget.NewButton("", func() { 
		dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) { 
			if r != nil { 
				// Logic run file
				defer r.Close(); term.Clear()
				data, _ := io.ReadAll(r)
				tmp := os.TempDir()+"/exec_tmp"
				os.WriteFile(tmp, data, 0755)
				isBin := bytes.HasPrefix(data, []byte("\x7fELF"))
				executeTask("", true, tmp, isBin)
				switchView("TERMINAL")
			} 
		}, w).Show() 
	})
	fdBtn.Importance = widget.LowImportance
	fab := container.NewVBox(layout.NewSpacer(), container.NewHBox(layout.NewSpacer(), container.NewGridWrap(fyne.NewSize(60,60), container.NewStack(fdImg, fdBtn))), widget.NewLabel(" "))

	rootStack := container.NewStack(
		contentContainer, // Changes between Terminal / GameTools
		triggerZone,      // Left edge swipe
		fab,              // Floating button
		menuStack,        // Side Menu Overlay
	)

	w.SetContent(rootStack)
	w.ShowAndRun()
}

