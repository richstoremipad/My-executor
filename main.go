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
   CONFIG & VARIABLES
========================================== */
const AppVersion = "1.1"
const ConfigURL = "https://raw.githubusercontent.com/tangsanrich/Fileku/main/executor.txt"
const CryptoKey = "RahasiaNegaraJanganSampaiBocorir"

// PERFORMANCE SETTINGS
const MaxDisplayRows = 50 
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
   TERMINAL ENGINE (ANTI-LAG / TAIL DROP)
========================================== */
type Terminal struct {
	grid          *widget.TextGrid
	scroll        *container.Scroll
	
	rawBuffer     bytes.Buffer
	bufMutex      sync.Mutex
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
	go term.renderLoop()
	return term
}

func (t *Terminal) Write(p []byte) (int, error) {
	t.bufMutex.Lock()
	if t.rawBuffer.Len() > 20000 { t.rawBuffer.Reset() } // Tail Drop Protection
	t.rawBuffer.Write(p)
	t.bufMutex.Unlock()
	
	// Navigasi Trigger
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
	t.cachedRows = []widget.TextGridRow{}
	t.bufMutex.Unlock()
	t.grid.SetText("")
}

func (t *Terminal) renderLoop() {
	ticker := time.NewTicker(RefreshRate)
	defer ticker.Stop()
	for range ticker.C { t.processBuffer() }
}

func (t *Terminal) processBuffer() {
	t.bufMutex.Lock()
	if t.rawBuffer.Len() == 0 { t.bufMutex.Unlock(); return }
	data := t.rawBuffer.String()
	t.rawBuffer.Reset()
	t.bufMutex.Unlock()

	if len(data) > 4000 { data = "...\n[SKIP DATA]\n..." + data[len(data)-4000:] }

	newRows := t.parseAnsiToRows(data)
	t.cachedRows = append(t.cachedRows, newRows...)
	if len(t.cachedRows) > MaxDisplayRows {
		t.cachedRows = t.cachedRows[len(t.cachedRows)-MaxDisplayRows:]
	}

	finalRows := make([]widget.TextGridRow, len(t.cachedRows))
	copy(finalRows, t.cachedRows)
	
	t.grid.Rows = finalRows
	t.grid.Refresh()
	t.scroll.ScrollToBottom()
}

func (t *Terminal) parseAnsiToRows(text string) []widget.TextGridRow {
	var rows []widget.TextGridRow
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	currentStyle := &widget.CustomTextGridStyle{FGColor: theme.ForegroundColor(), BGColor: color.Transparent}

	for _, line := range lines {
		row := widget.TextGridRow{Cells: []widget.TextGridCell{}}
		remaining := line
		for len(remaining) > 0 {
			loc := t.reAnsi.FindStringIndex(remaining)
			if loc == nil {
				row.Cells = append(row.Cells, t.stringToCells(remaining, currentStyle)...)
				break
			}
			if loc[0] > 0 {
				row.Cells = append(row.Cells, t.stringToCells(remaining[:loc[0]], currentStyle)...)
			}
			t.updateStyle(remaining[loc[0]:loc[1]], currentStyle)
			remaining = remaining[loc[1]:]
		}
		rows = append(rows, row)
	}
	return rows
}

func (t *Terminal) stringToCells(s string, style *widget.CustomTextGridStyle) []widget.TextGridCell {
	cells := make([]widget.TextGridCell, len(s))
	staticStyle := *style 
	for i, r := range s { cells[i] = widget.TextGridCell{Rune: r, Style: &staticStyle} }
	return cells
}

func (t *Terminal) updateStyle(codeSeq string, style *widget.CustomTextGridStyle) {
	if len(codeSeq) < 3 { return }
	content := codeSeq[2 : len(codeSeq)-1]
	parts := strings.Split(content, ";")
	for _, part := range parts {
		if part == "" || part == "0" { style.FGColor = theme.ForegroundColor() } else {
			col := ansiToColor(part); if col != nil { style.FGColor = col }
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
	
	go func() {
		time.Sleep(1 * time.Second)
		if !CheckRoot() { /* Info */ }
	}()
	if !CheckRoot() { currentDir = "/sdcard" }

	var overlayContainer *fyne.Container

	// 1. POPUP MODAL HELPER (DIGUNAKAN OLEH INJECT & UPDATE)
	showModal := func(title, msg, confirm string, action func(), isErr bool, isForce bool) {
		w.Canvas().Refresh(w.Content())
		cancelLabel := "BATAL"
		cancelFunc := func() { overlayContainer.Hide() }
		if isForce { cancelLabel = "KELUAR"; cancelFunc = func() { os.Exit(0) } }
		
		btnCancel := widget.NewButton(cancelLabel, cancelFunc); btnCancel.Importance = widget.DangerImportance
		btnOk := widget.NewButton(confirm, func() { if !isForce { overlayContainer.Hide() }; if action != nil { action() } })
		if confirm == "COBA LAGI" { btnOk.Importance = widget.HighImportance } else {
			if isErr { btnOk.Importance = widget.DangerImportance } else { btnOk.Importance = widget.HighImportance }
		}
		
		btnBox := container.NewHBox(layout.NewSpacer(), container.NewGridWrap(fyne.NewSize(110,40), btnCancel), widget.NewLabel("   "), container.NewGridWrap(fyne.NewSize(110,40), btnOk), layout.NewSpacer())
		lblTitle := createLabel(title, theme.ForegroundColor(), 18, true); if isErr { lblTitle.Color = theme.ErrorColor() }
		lblMsg := widget.NewLabel(msg); lblMsg.Alignment = fyne.TextAlignCenter; lblMsg.Wrapping = fyne.TextWrapWord
		
		content := container.NewVBox(container.NewPadded(container.NewCenter(lblTitle)), container.NewPadded(lblMsg), widget.NewLabel(""), btnBox)
		card := widget.NewCard("", "", container.NewPadded(content))
		wrapper := container.NewCenter(container.NewGridWrap(fyne.NewSize(300, 220), container.NewPadded(card)))
		overlayContainer.Objects = []fyne.CanvasObject{canvas.NewRectangle(color.RGBA{0,0,0,220}), wrapper}
		overlayContainer.Show(); overlayContainer.Refresh()
	}

	// 2. STATUS UI VARIABLES
	brightYellow := color.RGBA{R: 255, G: 255, B: 0, A: 255}
	successGreen := color.RGBA{R: 0, G: 255, B: 0, A: 255}
	failRed := color.RGBA{R: 255, G: 50, B: 50, A: 255}
	silverColor := color.Gray{Y: 180}

	status := canvas.NewText("System: Ready", silverColor); status.TextSize = 12; status.Alignment = fyne.TextAlignCenter
	lblKernelValue := createLabel("...", color.Gray{Y: 150}, 11, true)
	lblSELinuxValue := createLabel("...", color.Gray{Y: 150}, 11, true)
	lblSystemValue := createLabel("...", color.Gray{Y: 150}, 11, true)

	// 3. AUTO INSTALL KERNEL (MENGGUNAKAN archive/zip)
	autoInstallKernel := func() {
		term.Clear(); status.Text = "Install Driver..."; status.Refresh()
		go func() {
			targetKoPath := "/data/local/tmp/module_inject.ko"
			out, _ := exec.Command("uname", "-r").Output()
			fullVer := strings.TrimSpace(string(out)); targetVer := strings.Split(fullVer, "-")[0]
			
			// UNZIP LOGIC
			zipReader, err := zip.NewReader(bytes.NewReader(driverZip), int64(len(driverZip)))
			if err != nil { term.Write([]byte("Zip Error\n")); return }
			
			var fileToExtract *zip.File
			for _, f := range zipReader.File { if strings.HasSuffix(f.Name, ".ko") && strings.Contains(f.Name, targetVer) { fileToExtract = f; break } }
			if fileToExtract == nil { for _, f := range zipReader.File { if strings.HasSuffix(f.Name, ".ko") { fileToExtract = f; break } } }
			
			if fileToExtract != nil {
				rc, _ := fileToExtract.Open(); buf := new(bytes.Buffer); io.Copy(buf, rc); rc.Close()
				userTmp := filepath.Join(os.TempDir(), "temp_mod.ko")
				os.WriteFile(userTmp, buf.Bytes(), 0644)
				exec.Command("su", "-c", fmt.Sprintf("cp %s %s", userTmp, targetKoPath)).Run()
				exec.Command("su", "-c", "chmod 777 "+targetKoPath).Run()
				exec.Command("su", "-c", "insmod "+targetKoPath).Run()
				status.Text = "Done"; status.Refresh()
			} else {
				term.Write([]byte("Driver Not Found\n"))
			}
		}()
	}

	// 4. CHECK UPDATE (MENGGUNAKAN net/http, encoding/json, net/url)
	var checkUpdate func()
	checkUpdate = func() {
		overlayContainer.Hide()
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
					}
				}
			}
		} else {
			showModal("ERROR", "Koneksi Gagal.", "COBA LAGI", func() { go checkUpdate() }, true, true)
		}
	}
	// Jalankan Update check
	go func() { time.Sleep(1 * time.Second); checkUpdate() }()

	// 5. EXECUTE LOGIC
	executeTask := func(cmdText string, isScript bool, scriptPath string, isBinary bool) {
		status.Text = "Processing..."; status.Refresh()
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
					cmd = exec.Command("sh", "-c", cmdText); cmd.Dir = currentDir
				}
			}

			cmd.Env = append(os.Environ(), "TERM=xterm-256color")
			prOut, pwOut := io.Pipe(); prErr, pwErr := io.Pipe()
			cmd.Stdout = pwOut; cmd.Stderr = pwErr
			
			stdinPipe, _ := cmd.StdinPipe()
			cmdMutex.Lock(); activeStdin = stdinPipe; cmdMutex.Unlock()

			readerFunc := func(r io.Reader) {
				buf := make([]byte, 8192)
				for {
					n, err := r.Read(buf)
					if n > 0 { term.Write(buf[:n]) }
					if err != nil { break }
				}
			}

			if err := cmd.Start(); err != nil {
				term.Write([]byte(fmt.Sprintf("Error: %s\n", err.Error())))
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
			status.Text = "Idle"; status.Refresh()
			if isScript && isRoot { exec.Command("su", "-c", "rm -f /data/local/tmp/temp_exec").Run() }
		}()
	}

	input := widget.NewEntry(); input.SetPlaceHolder("Command...")
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
			term.Write([]byte(fmt.Sprintf("Dir > %s\n", currentDir)))
			return
		}
		executeTask(text, false, "", false)
	}
	input.OnSubmitted = func(_ string) { send() }

	// ================= NAVIGASI =================
	var navFloatContainer *fyne.Container
	sendKey := func(data string) {
		cmdMutex.Lock(); defer cmdMutex.Unlock()
		if activeStdin != nil { io.WriteString(activeStdin, data) }
	}
	
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

	// ================= MONITORING LOOP =================
	go func() {
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
	lblKernelTitle := createLabel("KERNEL", brightYellow, 10, true)
	lblSELinuxTitle := createLabel("SELINUX", brightYellow, 10, true)
	lblSystemTitle := createLabel("ROOT", brightYellow, 10, true)
	
	infoGrid := container.NewGridWithColumns(3, container.NewVBox(lblKernelTitle, lblKernelValue), container.NewVBox(lblSELinuxTitle, lblSELinuxValue), container.NewVBox(lblSystemTitle, lblSystemValue))
	
	btnInj := widget.NewButtonWithIcon("Inject", theme.DownloadIcon(), func() { showModal("INJECT", "Inject Driver?", "MULAI", autoInstallKernel, false, false) })
	btnSel := widget.NewButtonWithIcon("SELinux", theme.ViewRefreshIcon(), func() { go func() { exec.Command("su","-c","setenforce 0").Run() }() })
	btnClr := widget.NewButtonWithIcon("Clear", theme.ContentClearIcon(), func() { term.Clear() }); btnClr.Importance = widget.DangerImportance
	
	header := container.NewStack(canvas.NewRectangle(color.Gray{Y: 45}), container.NewVBox(container.NewPadded(createLabel("EXECUTOR", color.White, 16, true)), container.NewPadded(infoGrid), container.NewPadded(container.NewGridWithColumns(3, btnInj, btnSel, btnClr)), container.NewPadded(status), widget.NewSeparator()))
	
	bottomArea := container.NewVBox(container.NewPadded(createLabel("Code by TANGSAN", silverColor, 10, false)), container.NewPadded(container.NewBorder(nil, nil, nil, widget.NewButtonWithIcon("", theme.MailSendIcon(), send), input)))

	bg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "bg.png", StaticContent: bgPng}); bg.FillMode = canvas.ImageFillStretch
	termBox := container.NewStack(canvas.NewRectangle(color.Black), bg, canvas.NewRectangle(color.RGBA{0,0,0,180}), term.scroll)
	
	fdImg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "fd.png", StaticContent: fdPng}); fdImg.FillMode = canvas.ImageFillContain
	fdBtn := widget.NewButton("", func() { dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) { if r != nil { runFile(r) } }, w).Show() }); fdBtn.Importance = widget.LowImportance
	fab := container.NewVBox(layout.NewSpacer(), container.NewPadded(container.NewHBox(layout.NewSpacer(), container.NewGridWrap(fyne.NewSize(65,65), container.NewStack(container.NewPadded(fdImg), fdBtn)))), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "))

	overlayContainer = container.NewStack(); overlayContainer.Hide()
	w.SetContent(container.NewStack(container.NewBorder(header, bottomArea, nil, nil, termBox), fab, navFloatContainer, overlayContainer))
	w.ShowAndRun()
}

func createLabel(text string, color color.Color, size float32, bold bool) *canvas.Text {
	lbl := canvas.NewText(text, color); lbl.TextSize = size; lbl.Alignment = fyne.TextAlignCenter; if bold { lbl.TextStyle = fyne.TextStyle{Bold: true} }; return lbl
}

