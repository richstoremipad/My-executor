package main

import (
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
   CONFIG & UPDATE SYSTEM
========================================== */
const AppVersion = "1.0"
const GitHubRepo = "https://raw.githubusercontent.com/tangsanrich/Fileku/main/Driver/"
const FlagFile = "/dev/status_driver_aktif"
const TargetDriverName = "5.10_A12"
const ConfigURL = "https://raw.githubusercontent.com/tangsanrich/Fileku/main/executor.txt"
const CryptoKey = "RahasiaNegaraJanganSampaiBocorir"

// Global Variable
var currentDir string = "/sdcard" // Default ke sdcard agar user langsung lihat file
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

/* ==========================================
   SECURITY LOGIC
========================================== */
func decryptConfig(encryptedStr string) ([]byte, error) {
	defer func() {
		if r := recover(); r != nil {}
	}()
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
   TERMINAL LOGIC (HIGH PERFORMANCE)
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
	// Render Throttling (20 FPS)
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
			if part == "" || part == "0" {
				t.curStyle.FGColor = theme.ForegroundColor()
			} else {
				col := ansiToColor(part)
				if col != nil { t.curStyle.FGColor = col }
			}
		}
	case 'J':
		if strings.Contains(content, "2") { t.grid.SetText(""); t.curRow = 0; t.curCol = 0 }
	case 'H':
		t.curRow = 0; t.curCol = 0
	}
}

func (t *Terminal) printText(text string) {
	for _, char := range text {
		if char == '\n' { t.curRow++; t.curCol = 0; continue }
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

func CheckKernelDriver() bool {
	cmd := exec.Command("su", "-c", "ls -d /sys/module/"+TargetDriverName)
	return cmd.Run() == nil
}

func CheckSELinux() string {
	cmd := exec.Command("su", "-c", "getenforce")
	out, err := cmd.Output()
	if err != nil { return "Unknown" }
	return strings.TrimSpace(string(out))
}

func VerifySuccessAndCreateFlag() bool {
	if exec.Command("su", "-c", "ls -d /sys/module/"+TargetDriverName).Run() == nil {
		exec.Command("su", "-c", "touch "+FlagFile).Run()
		exec.Command("su", "-c", "chmod 777 "+FlagFile).Run()
		return true
	}
	return false
}

// Request Permission Helper for Android 11+
func RequestStoragePermission() {
	// Try to open "Manage All Files Access" settings
	// This command works on non-root devices to trigger the settings intent
	cmd := exec.Command("am", "start", "-a", "android.settings.MANAGE_ALL_FILES_ACCESS_PERMISSION")
	cmd.Run()
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
	
	// Force Permission Request on Startup
	go func() {
		time.Sleep(1 * time.Second)
		if !CheckRoot() {
			RequestStoragePermission()
		}
	}()

	// Init Path
	if !CheckRoot() {
		// Default to sdcard for better UX on non-root
		currentDir = "/sdcard"
	}

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
					if !isBinary {
						cmd = exec.Command("su", "-c", "sh "+target)
					} else {
						cmd = exec.Command("su", "-c", target)
					}
				} else {
					// Non-Root Smart Execute
					if !isBinary {
						cmd = exec.Command("sh", scriptPath)
					} else {
						cmd = exec.Command(scriptPath)
					}
				}
			} else {
				if isRoot {
					cmd = exec.Command("su", "-c", fmt.Sprintf("cd \"%s\" && %s", currentDir, cmdText))
				} else {
					// Non-Root Command Logic with Hacks
					runCmd := cmdText
					
					// FIX: Always use ls -a to force show hidden/all files
					if strings.HasPrefix(cmdText, "ls") {
						if !strings.Contains(cmdText, "-a") {
							runCmd = strings.Replace(cmdText, "ls", "ls -a", 1)
						}
					}

					// FIX: Handle ./file execution
					if strings.HasPrefix(cmdText, "./") {
						fileName := strings.TrimPrefix(cmdText, "./")
						fullPath := filepath.Join(currentDir, fileName)
						
						if strings.HasSuffix(fileName, ".sh") {
							runCmd = fmt.Sprintf("sh \"%s\"", fullPath)
						} else {
							// Binary fallback
							tmpBin := filepath.Join(os.TempDir(), fileName)
							if err := copyFile(fullPath, tmpBin); err == nil {
								os.Chmod(tmpBin, 0755)
								runCmd = tmpBin 
							} else {
								runCmd = fullPath
							}
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

			cmdMutex.Lock()
			activeStdin = stdin
			cmdMutex.Unlock()

			if err := cmd.Start(); err != nil {
				term.Write([]byte(fmt.Sprintf("\x1b[31mError: %s\x1b[0m\n", err.Error())))
				cmdMutex.Lock(); activeStdin = nil; cmdMutex.Unlock()
				return
			}

			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); io.Copy(term, stdout) }()
			go func() { defer wg.Done(); io.Copy(term, stderr) }()
			
			wg.Wait()
			cmd.Wait()

			cmdMutex.Lock()
			activeStdin = nil
			cmdMutex.Unlock()
			
			status.Text = "Status: Idle"
			status.Refresh()
			
			if isScript && isRoot { exec.Command("su", "-c", "rm -f /data/local/tmp/temp_exec").Run() }
		}()
	}

	send := func() {
		text := input.Text
		input.SetText("")
		cmdMutex.Lock()
		if activeStdin != nil {
			io.WriteString(activeStdin, text+"\n")
			term.Write([]byte(text + "\n"))
			cmdMutex.Unlock()
			return
		}
		cmdMutex.Unlock()
		if strings.TrimSpace(text) == "" { return }
		
		if strings.HasPrefix(text, "cd") {
			parts := strings.Fields(text)
			newPath := currentDir
			if len(parts) == 1 {
				if CheckRoot() { newPath = "/data/local/tmp" } else { h, _ := os.UserHomeDir(); newPath = h }
			} else {
				arg := parts[1]
				if filepath.IsAbs(arg) { newPath = arg } else { newPath = filepath.Join(currentDir, arg) }
			}
			newPath = filepath.Clean(newPath)
			exist := false
			if CheckRoot() {
				if exec.Command("su", "-c", "[ -d \""+newPath+"\" ]").Run() == nil { exist = true }
			} else {
				if info, err := os.Stat(newPath); err == nil && info.IsDir() { exist = true }
			}
			if exist {
				currentDir = newPath
				displayDir := currentDir
				if len(displayDir) > 25 { displayDir = "..." + displayDir[len(displayDir)-20:] }
				term.Write([]byte(fmt.Sprintf("\x1b[33m%s \x1b[36m> \x1b[0mcd %s\n", displayDir, parts[1])))
			} else {
				term.Write([]byte(fmt.Sprintf("\x1b[31mcd: %s: No such directory\x1b[0m\n", parts[1])))
			}
			return
		}
		executeTask(text, false, "", false)
	}

	input.OnSubmitted = func(_ string) { send() }

	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		term.Clear()
		data, err := io.ReadAll(reader)
		if err != nil { term.Write([]byte("\x1b[31m[ERR] Read Failed\x1b[0m\n")); return }
		
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))
		
		tmpFile, err := os.CreateTemp("", "exec_tmp")
		if err != nil { term.Write([]byte("\x1b[31m[ERR] Write Failed\x1b[0m\n")); return }
		tmpPath := tmpFile.Name()
		tmpFile.Write(data)
		tmpFile.Close()
		os.Chmod(tmpPath, 0755)
		
		executeTask("", true, tmpPath, isBinary)
	}

	var overlayContainer *fyne.Container
	showModal := func(title, msg, confirm string, action func(), isErr bool) {
		w.Canvas().Refresh(w.Content())
		btnCancel := widget.NewButton("CANCEL", func() { overlayContainer.Hide() })
		btnCancel.Importance = widget.DangerImportance
		btnOk := widget.NewButton(confirm, func() { overlayContainer.Hide(); if action != nil { action() } })
		if isErr { btnOk.Importance = widget.DangerImportance } else { btnOk.Importance = widget.HighImportance }
		btnBox := container.NewHBox(layout.NewSpacer(), container.NewGridWrap(fyne.NewSize(110,40), btnCancel), widget.NewLabel("   "), container.NewGridWrap(fyne.NewSize(110,40), btnOk), layout.NewSpacer())
		lblTitle := createLabel(title, theme.ForegroundColor(), 18, true)
		if isErr { lblTitle.Color = theme.ErrorColor() }
		content := container.NewVBox(container.NewPadded(container.NewCenter(lblTitle)), widget.NewLabel(msg), widget.NewLabel(""), btnBox)
		card := widget.NewCard("", "", container.NewPadded(content))
		wrapper := container.NewCenter(container.NewGridWrap(fyne.NewSize(300, 220), container.NewPadded(card)))
		overlayContainer.Objects = []fyne.CanvasObject{canvas.NewRectangle(color.RGBA{0,0,0,220}), wrapper}
		overlayContainer.Show(); overlayContainer.Refresh()
	}

	go func() {
		time.Sleep(1500 * time.Millisecond)
		if strings.Contains(ConfigURL, "GANTI") { term.Write([]byte("\n\x1b[33m[WARN] ConfigURL!\x1b[0m\n")); return }
		term.Write([]byte("\n\x1b[90m[*] Checking updates...\x1b[0m\n"))
		client := &http.Client{Timeout: 10 * time.Second}
		if resp, err := client.Get(fmt.Sprintf("%s?v=%d", ConfigURL, time.Now().Unix())); err == nil && resp.StatusCode == 200 {
			body, _ := io.ReadAll(resp.Body); resp.Body.Close()
			if dec, err := decryptConfig(string(bytes.TrimSpace(body))); err == nil {
				var cfg OnlineConfig
				if json.Unmarshal(dec, &cfg) == nil && cfg.Version != "" && cfg.Version != AppVersion {
					showModal("UPDATE", cfg.Message, "UPDATE", func() { if u, e := url.Parse(cfg.Link); e == nil { app.New().OpenURL(u) } }, false)
				} else {
					term.Write([]byte("\x1b[32m[V] System Updated.\x1b[0m\n"))
				}
			}
		} else {
			term.Write([]byte("\x1b[31m[ERR] Net/Server Fail\x1b[0m\n"))
		}
	}()

	autoInstallKernel := func() {
		term.Clear(); status.Text = "System: Installing..."; status.Refresh()
		go func() {
			exec.Command("su", "-c", "rm -f "+FlagFile).Run()
			term.Write([]byte("\x1b[36m╔════ DRIVER INSTALLER ════╗\x1b[0m\n"))
			out, _ := exec.Command("uname", "-r").Output()
			ver := strings.TrimSpace(string(out))
			term.Write([]byte(fmt.Sprintf("Target: \x1b[33m%s\x1b[0m\n", ver)))
			
			dlPath := "/data/local/tmp/temp_kernel_dl"; target := "/data/local/tmp/installer.sh"
			var dlUrl string; var found bool = false
			
			simulateProcess := func(label string) {
				for i := 0; i <= 100; i += 10 { drawProgressBar(term, label, i, "\x1b[36m"); time.Sleep(50 * time.Millisecond) }
				term.Write([]byte("\n"))
			}

			term.Write([]byte("\x1b[97m[*] Checking Repository (Variant 1)...\x1b[0m\n"))
			simulateProcess("Connecting...")
			url1 := GitHubRepo + ver + ".sh"
			
			err, _ := downloadFile(url1, dlPath)
			if err == nil { dlUrl = "Variant 1"; found = true }
			
			if !found {
				parts := strings.Split(ver, "-"); 
				if len(parts) > 0 {
					term.Write([]byte("\n\x1b[97m[*] Checking Repository (Variant 2)...\x1b[0m\n"))
					simulateProcess("Connecting...")
					url2 := GitHubRepo + parts[0] + ".sh"
					err, _ = downloadFile(url2, dlPath)
					if err == nil { dlUrl = "Variant 2"; found = true }
				}
			}

			if !found {
				term.Write([]byte("\n\x1b[31m[DRIVER NOT FOUND]\x1b[0m\n")); status.Text = "Failed"; status.Refresh()
			} else {
				term.Write([]byte("\n\x1b[92m[*] Downloading Script: " + dlUrl + "\x1b[0m\n"))
				simulateProcess("Downloading Payload")
				exec.Command("su", "-c", "mv "+dlPath+" "+target).Run()
				exec.Command("su", "-c", "chmod 777 "+target).Run()
				executeTask("", true, target, false)
				VerifySuccessAndCreateFlag()
			}
		}()
	}

	titleText := createLabel("SIMPLE EXEC", color.White, 16, true)
	infoGrid := container.NewGridWithColumns(3, container.NewVBox(lblKernelTitle, lblKernelValue), container.NewVBox(lblSELinuxTitle, lblSELinuxValue), container.NewVBox(lblSystemTitle, lblSystemValue))
	
	btnInj := widget.NewButtonWithIcon("Inject", theme.DownloadIcon(), func() { showModal("INJECT", "Start injection?", "START", autoInstallKernel, false) }); btnInj.Importance = widget.HighImportance
	btnSel := widget.NewButtonWithIcon("SELinux", theme.ViewRefreshIcon(), func() { go func() { if CheckSELinux()=="Enforcing" { exec.Command("su","-c","setenforce 0").Run() } else { exec.Command("su","-c","setenforce 1").Run() } }() }); btnSel.Importance = widget.HighImportance
	btnClr := widget.NewButtonWithIcon("Clear", theme.ContentClearIcon(), func() { term.Clear() }); btnClr.Importance = widget.DangerImportance
	
	// ADD PERMISSION BUTTON
	btnPerm := widget.NewButtonWithIcon("", theme.SettingsIcon(), func() { RequestStoragePermission() })
	btnPerm.Importance = widget.LowImportance
	
	// Header Adjusted
	headerTop := container.NewBorder(nil, nil, nil, container.NewGridWrap(fyne.NewSize(40,30), btnPerm), container.NewPadded(titleText))
	
	header := container.NewStack(canvas.NewRectangle(color.Gray{Y: 45}), container.NewVBox(
		headerTop,
		container.NewPadded(infoGrid),
		container.NewPadded(container.NewGridWithColumns(3, btnInj, btnSel, btnClr)),
		container.NewPadded(status),
		widget.NewSeparator(),
	))

	sendBtn := widget.NewButtonWithIcon("", theme.MailSendIcon(), send)
	cpyLbl := createLabel("Code by TANGSAN", silverColor, 10, false)
	bottom := container.NewVBox(container.NewPadded(cpyLbl), container.NewPadded(container.NewBorder(nil, nil, nil, sendBtn, input)))
	bg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "bg.png", StaticContent: bgPng}); bg.FillMode = canvas.ImageFillStretch
	termBox := container.NewStack(canvas.NewRectangle(color.Black), bg, canvas.NewRectangle(color.RGBA{0,0,0,180}), term.scroll)
	fdImg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "fd.png", StaticContent: fdPng}); fdImg.FillMode = canvas.ImageFillContain
	fdBtn := widget.NewButton("", func() { dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) { if r != nil { runFile(r) } }, w).Show() }); fdBtn.Importance = widget.LowImportance
	fab := container.NewVBox(layout.NewSpacer(), container.NewPadded(container.NewHBox(layout.NewSpacer(), container.NewGridWrap(fyne.NewSize(65,65), container.NewStack(container.NewPadded(fdImg), fdBtn)))), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "))
	overlayContainer = container.NewStack(); overlayContainer.Hide()
	w.SetContent(container.NewStack(container.NewBorder(header, bottom, nil, nil, termBox), fab, overlayContainer))
	w.ShowAndRun()
}

