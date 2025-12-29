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
   CONFIG
========================================== */
const AppVersion = "1.0"
const ConfigURL = "https://raw.githubusercontent.com/tangsanrich/Fileku/main/executor.txt"
const CryptoKey = "RahasiaNegaraJanganSampaiBocorir"
const MaxScrollback = 200

var currentDir string = "/sdcard"
var activeStdin io.WriteCloser
var cmdMutex sync.Mutex
var globalStatus *canvas.Text

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
   GESTURE OVERLAY (SWIPE MENU)
========================================== */
type GestureOverlay struct {
	widget.BaseWidget
	dragAccumY  float32
	onSwipeUp   func()
	onSwipeDown func()
	onLongPress func()
}

func NewGestureOverlay(up, down, longPress func()) *GestureOverlay {
	g := &GestureOverlay{onSwipeUp: up, onSwipeDown: down, onLongPress: longPress}
	g.ExtendBaseWidget(g)
	return g
}

func (g *GestureOverlay) OnDragStart() { g.dragAccumY = 0 }
func (g *GestureOverlay) Dragged(e *fyne.DragEvent) {
	g.dragAccumY += e.Dragged.DY
	// Threshold disesuaikan agar tidak terlalu sensitif (mencegah jitter)
	threshold := float32(30.0) 

	if g.dragAccumY > threshold {
		if g.onSwipeDown != nil { g.onSwipeDown() }
		g.dragAccumY = 0
	} else if g.dragAccumY < -threshold {
		if g.onSwipeUp != nil { g.onSwipeUp() }
		g.dragAccumY = 0
	}
}
func (g *GestureOverlay) DragEnd() { g.dragAccumY = 0 }
func (g *GestureOverlay) TappedSecondary(e *fyne.PointEvent) { if g.onLongPress != nil { g.onLongPress() } }
func (g *GestureOverlay) Tapped(e *fyne.PointEvent) {}
func (g *GestureOverlay) TouchDown(e *mobile.TouchEvent) {}
func (g *GestureOverlay) TouchUp(e *mobile.TouchEvent) {}
func (g *GestureOverlay) TouchCancel(e *mobile.TouchEvent) {}
func (g *GestureOverlay) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(canvas.NewRectangle(color.Transparent))
}

/* ==========================================
   TERMINAL LOGIC (ANTI-FLICKER + BUFFERED)
========================================== */
type Terminal struct {
	grid         *widget.TextGrid
	scroll       *container.Scroll
	rows         [][]widget.TextGridCell
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
	
	term := &Terminal{
		grid:     g,
		scroll:   container.NewScroll(g),
		rows:     make([][]widget.TextGridCell, 0, MaxScrollback),
		curRow:   0,
		curCol:   0,
		curStyle: &widget.CustomTextGridStyle{FGColor: theme.ForegroundColor(), BGColor: color.Transparent},
		reAnsi:   regexp.MustCompile(`\x1b\[([0-9;]*)?([a-zA-Z])`),
	}

	// RENDER LOOP (FIXED FLICKERING)
	go func() {
		ticker := time.NewTicker(33 * time.Millisecond) // ~30 FPS
		for range ticker.C {
			term.mutex.Lock()
			if term.needsRefresh {
				// LOGIKA BARU: Update Langsung (Tanpa Reset Kosong)
				
				// 1. Siapkan slice baris UI baru sesuai jumlah data
				newRows := make([]widget.TextGridRow, len(term.rows))
				
				// 2. Copy data dari memori ke struct UI
				for i, r := range term.rows {
					newRows[i] = widget.TextGridRow{Cells: r}
				}
				
				// 3. Terapkan ke Grid (Atomic update)
				// Kita TIDAK memanggil term.grid.SetText("") agar tidak kedip
				term.grid.Rows = newRows
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
	case "0": return theme.ForegroundColor()
	case "1": return color.White
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

func (t *Terminal) Clear() {
	t.mutex.Lock()
	t.rows = make([][]widget.TextGridCell, 0)
	t.curRow = 0
	t.curCol = 0
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
		if loc == nil {
			t.printTextMem(raw)
			break
		}
		if loc[0] > 0 {
			t.printTextMem(raw[:loc[0]])
		}
		t.handleAnsiMem(raw[loc[0]:loc[1]])
		raw = raw[loc[1]:]
	}

	t.needsRefresh = true
	return len(p), nil
}

func (t *Terminal) handleAnsiMem(codeSeq string) {
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
		if strings.Contains(content, "2") {
			t.rows = make([][]widget.TextGridCell, 0)
			t.curRow = 0; t.curCol = 0
		}
	case 'H':
		t.curRow = 0; t.curCol = 0
	}
}

func (t *Terminal) printTextMem(text string) {
	for _, char := range text {
		if char == '\n' {
			t.curRow++
			t.curCol = 0
			if len(t.rows) > MaxScrollback {
				t.rows = t.rows[1:]
				t.curRow--
			}
			continue
		}
		if char == '\r' { t.curCol = 0; continue }

		for t.curRow >= len(t.rows) {
			t.rows = append(t.rows, []widget.TextGridCell{})
		}
		
		row := &t.rows[t.curRow]
		
		// Pastikan kapasitas row cukup (Anti-Crash)
		// Jika kolom melompat jauh (misal tab), isi kekosongan
		if t.curCol >= len(*row) {
			needed := t.curCol + 1 - len(*row)
			padding := make([]widget.TextGridCell, needed)
			*row = append(*row, padding...)
		}

		// Update cell langsung di memori
		styleCopy := *t.curStyle
		(*row)[t.curCol] = widget.TextGridCell{Rune: char, Style: &styleCopy}
		t.curCol++
	}
}

func (t *Terminal) GetContent() string {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	var sb strings.Builder
	for _, row := range t.rows {
		for _, cell := range row {
			if cell.Rune == 0 {
				sb.WriteRune(' ')
			} else {
				sb.WriteRune(cell.Rune)
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

/* ===============================
   SYSTEM FUNCTIONS
================================ */
func CheckRoot() bool {
	cmd := exec.Command("su", "-c", "id -u")
	out, err := cmd.Output()
	if err != nil { return false }
	return strings.TrimSpace(string(out)) == "0"
}

func CheckKernelDriver() bool {
	return exec.Command("su", "-c", "grep -q 'read_physical_address' /proc/kallsyms").Run() == nil
}

func CheckSELinux() string {
	out, _ := exec.Command("su", "-c", "getenforce").Output()
	return strings.TrimSpace(string(out))
}

func decryptConfig(encryptedStr string) ([]byte, error) {
	defer func() { recover() }()
	key := []byte(CryptoKey)
	if len(key) != 32 { return nil, errors.New("key len") }
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encryptedStr))
	if err != nil { return nil, err }
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	if len(data) < gcm.NonceSize() { return nil, errors.New("corrupt") }
	return gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
}

func createLabel(text string, c color.Color, size float32, bold bool) *canvas.Text {
	lbl := canvas.NewText(text, c)
	lbl.TextSize = size; lbl.Alignment = fyne.TextAlignCenter
	if bold { lbl.TextStyle = fyne.TextStyle{Bold: true} }
	return lbl
}

/* ===============================
              MAIN UI
================================ */
func main() {
	a := app.New()
	a.Settings().SetTheme(theme.DarkTheme())

	w := a.NewWindow("Simple Exec")
	w.Resize(fyne.NewSize(400, 700))
	w.SetMaster()

	term := NewTerminal()
	
	go func() { time.Sleep(1*time.Second); CheckRoot() }()
	if !CheckRoot() { currentDir = "/sdcard" }

	brightYellow := color.RGBA{255, 255, 0, 255}
	successGreen := color.RGBA{0, 255, 0, 255}
	failRed := color.RGBA{255, 50, 50, 255}
	silverColor := color.Gray{Y: 180}

	status := canvas.NewText("System: Ready", silverColor)
	status.TextSize = 12; status.Alignment = fyne.TextAlignCenter
	globalStatus = status

	titleText := createLabel("SIMPLE EXECUTOR", color.White, 16, true)
	input := widget.NewEntry(); input.SetPlaceHolder("Terminal Command...")

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
				defer func() { recover() }()
				if CheckRoot() { lblSystemValue.Text="GRANTED"; lblSystemValue.Color=successGreen } else { lblSystemValue.Text="DENIED"; lblSystemValue.Color=failRed }
				lblSystemValue.Refresh()
				
				if CheckKernelDriver() { lblKernelValue.Text="ACTIVE"; lblKernelValue.Color=successGreen } else { lblKernelValue.Text="MISSING"; lblKernelValue.Color=failRed }
				lblKernelValue.Refresh()
				
				se := CheckSELinux()
				lblSELinuxValue.Text = strings.ToUpper(se)
				if se == "Enforcing" { lblSELinuxValue.Color=successGreen } else { lblSELinuxValue.Color=failRed }
				lblSELinuxValue.Refresh()
			}()
			time.Sleep(3 * time.Second)
		}
	}()

	executeTask := func(cmdText string, isScript bool, scriptPath string, isBinary bool) {
		status.Text = "Status: Processing..."
		status.Color = silverColor
		status.Refresh()

		if !isScript {
			term.Write([]byte(fmt.Sprintf("\x1b[33m%s > \x1b[0m%s\n", currentDir, cmdText)))
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
			} else {
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); io.Copy(term, stdout) }()
				go func() { defer wg.Done(); io.Copy(term, stderr) }()
				wg.Wait(); cmd.Wait()
			}
			
			cmdMutex.Lock(); activeStdin = nil; cmdMutex.Unlock()
			status.Text = "Status: Idle"; status.Refresh()
			if isScript && isRoot { exec.Command("su", "-c", "rm -f /data/local/tmp/temp_exec").Run() }
		}()
	}

	send := func() {
		text := input.Text
		input.SetText("")
		cmdMutex.Lock()
		if activeStdin != nil { io.WriteString(activeStdin, text+"\n"); cmdMutex.Unlock(); return }
		cmdMutex.Unlock()

		if strings.TrimSpace(text) == "" { return }
		if strings.HasPrefix(text, "cd") {
			parts := strings.Fields(text)
			if len(parts) > 1 {
				newPath := filepath.Clean(filepath.Join(currentDir, parts[1]))
				if filepath.IsAbs(parts[1]) { newPath = parts[1] }
				currentDir = newPath
				term.Write([]byte(fmt.Sprintf("\x1b[33mCD > \x1b[0m%s\n", currentDir)))
			}
			return
		}
		executeTask(text, false, "", false)
	}
	input.OnSubmitted = func(_ string) { send() }

	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close(); term.Clear()
		data, _ := io.ReadAll(reader)
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))
		tmpFile, _ := os.CreateTemp("", "exec_tmp")
		tmpPath := tmpFile.Name()
		tmpFile.Write(data); tmpFile.Close(); os.Chmod(tmpPath, 0755)
		executeTask("", true, tmpPath, isBinary)
	}

	actionSwipeUp := func() {
		cmdMutex.Lock(); defer cmdMutex.Unlock()
		if activeStdin != nil { io.WriteString(activeStdin, "\x1b[B") }
	}
	actionSwipeDown := func() {
		cmdMutex.Lock(); defer cmdMutex.Unlock()
		if activeStdin != nil { io.WriteString(activeStdin, "\x1b[A") }
	}
	actionCopy := func() {
		w.Clipboard().SetContent(term.GetContent())
		status.Text = "Teks Disalin ke Clipboard!"; status.Color = successGreen; status.Refresh()
		go func() { time.Sleep(2*time.Second); status.Text="System: Ready"; status.Color=silverColor; status.Refresh() }()
	}

	gestureOverlay := NewGestureOverlay(actionSwipeUp, actionSwipeDown, actionCopy)
	
	var overlayContainer *fyne.Container
	overlayContainer = container.NewStack(); overlayContainer.Hide()

	showModal := func(title, msg, confirm string, action func(), isErr bool, isForce bool) {
		cancelLabel := "BATAL"; cancelFunc := func() { overlayContainer.Hide() }
		if isForce { cancelLabel = "KELUAR"; cancelFunc = func() { os.Exit(0) } }
		
		btnCancel := widget.NewButton(cancelLabel, cancelFunc); btnCancel.Importance = widget.DangerImportance
		btnOk := widget.NewButton(confirm, func() { if !isForce { overlayContainer.Hide() }; if action!=nil { action() } })
		if confirm=="COBA LAGI" { btnOk.Importance=widget.HighImportance } else if isErr { btnOk.Importance=widget.DangerImportance } else { btnOk.Importance=widget.HighImportance }

		content := container.NewVBox(
			container.NewPadded(container.NewCenter(createLabel(title, theme.ForegroundColor(), 18, true))),
			container.NewPadded(widget.NewLabelWithStyle(msg, fyne.TextAlignCenter, fyne.TextStyle{})),
			widget.NewLabel(""),
			container.NewHBox(layout.NewSpacer(), container.NewGridWrap(fyne.NewSize(110,40), btnCancel), widget.NewLabel(" "), container.NewGridWrap(fyne.NewSize(110,40), btnOk), layout.NewSpacer()),
		)
		card := widget.NewCard("", "", container.NewPadded(content))
		overlayContainer.Objects = []fyne.CanvasObject{canvas.NewRectangle(color.RGBA{0,0,0,220}), container.NewCenter(container.NewGridWrap(fyne.NewSize(300, 220), container.NewPadded(card)))}
		overlayContainer.Show(); overlayContainer.Refresh()
	}

	autoInstallKernel := func() {
		term.Clear(); status.Text="Installing..."; status.Refresh()
		go func() {
			term.Write([]byte("\x1b[36m╔════ DRIVER INSTALLER ════╗\x1b[0m\n"))
			out, _ := exec.Command("uname", "-r").Output()
			fullVer := strings.TrimSpace(string(out)); targetVer := strings.Split(fullVer, "-")[0]
			term.Write([]byte(fmt.Sprintf("Kernel: \x1b[33m%s\x1b[0m\n", fullVer)))
			
			target := "/data/local/tmp/mod.ko"
			exec.Command("su", "-c", "rm -f "+target).Run()
			
			r, err := zip.NewReader(bytes.NewReader(driverZip), int64(len(driverZip)))
			if err != nil { term.Write([]byte("\x1b[31m[ERR] Zip Fail\x1b[0m\n")); return }
			
			var f *zip.File
			for _, file := range r.File { if strings.HasSuffix(file.Name, ".ko") && strings.Contains(file.Name, targetVer) { f = file; break } }
			if f == nil { for _, file := range r.File { if strings.HasSuffix(file.Name, ".ko") { f = file; break } } }
			
			if f == nil { term.Write([]byte("\x1b[31m[FAIL] Not Found\x1b[0m\n")); return }
			
			term.Write([]byte(fmt.Sprintf("\x1b[32m[+] Extract: %s\x1b[0m\n", f.Name)))
			rc, _ := f.Open()
			buf := new(bytes.Buffer); io.Copy(buf, rc); rc.Close()
			tmp := filepath.Join(os.TempDir(), "tmp.ko")
			os.WriteFile(tmp, buf.Bytes(), 0644)
			exec.Command("su", "-c", fmt.Sprintf("cp %s %s && chmod 777 %s", tmp, target, target)).Run()
			os.Remove(tmp)

			term.Write([]byte("\x1b[36m[*] Injecting...\x1b[0m\n"))
			out, err = exec.Command("su", "-c", "insmod "+target).CombinedOutput()
			msg := string(out)
			if err == nil {
				term.Write([]byte("\x1b[92m[SUCCESS] Installed!\x1b[0m\n"))
				lblKernelValue.Text="ACTIVE"; lblKernelValue.Color=successGreen
			} else if strings.Contains(msg, "File exists") {
				term.Write([]byte("\x1b[33m[INFO] Already Active\x1b[0m\n"))
				lblKernelValue.Text="ACTIVE"; lblKernelValue.Color=successGreen
			} else {
				term.Write([]byte("\x1b[31m[FAIL] "+msg+"\x1b[0m\n"))
				lblKernelValue.Text="ERROR"; lblKernelValue.Color=failRed
			}
			lblKernelValue.Refresh(); status.Text="Done"; status.Refresh()
			exec.Command("su", "-c", "rm -f "+target).Run()
		}()
	}

	var checkUpdate func()
	checkUpdate = func() {
		overlayContainer.Hide(); time.Sleep(500 * time.Millisecond)
		if strings.Contains(ConfigURL, "GANTI") { return }
		
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(fmt.Sprintf("%s?v=%d", ConfigURL, time.Now().Unix()))
		if err == nil && resp.StatusCode == 200 {
			b, _ := io.ReadAll(resp.Body); resp.Body.Close()
			if dec, err := decryptConfig(string(bytes.TrimSpace(b))); err == nil {
				var cfg OnlineConfig
				if json.Unmarshal(dec, &cfg) == nil && cfg.Version != "" && cfg.Version != AppVersion {
					showModal("UPDATE", cfg.Message, "UPDATE", func() { if u, e := url.Parse(cfg.Link); e == nil { app.New().OpenURL(u) } }, false, true)
				}
			}
		} else {
			showModal("ERROR", "Gagal terhubung ke server.\nCek koneksi internet.", "COBA LAGI", func() { go checkUpdate() }, true, true)
		}
	}
	go func() { time.Sleep(1500 * time.Millisecond); checkUpdate() }()

	btnInj := widget.NewButtonWithIcon("Inject", theme.DownloadIcon(), func() { showModal("INJECT", "Inject Driver?", "MULAI", autoInstallKernel, false, false) })
	btnInj.Importance = widget.HighImportance
	btnSel := widget.NewButtonWithIcon("SELinux", theme.ViewRefreshIcon(), func() { 
		go func() {
			if CheckSELinux()=="Enforcing" { exec.Command("su","-c","setenforce 0").Run() } else { exec.Command("su","-c","setenforce 1").Run() }
			time.Sleep(100*time.Millisecond)
			s:=CheckSELinux(); lblSELinuxValue.Text=strings.ToUpper(s)
			if s=="Enforcing" { lblSELinuxValue.Color=successGreen } else { lblSELinuxValue.Color=failRed }
			lblSELinuxValue.Refresh()
		}() 
	})
	btnSel.Importance = widget.HighImportance
	btnClr := widget.NewButtonWithIcon("Clear", theme.ContentClearIcon(), func() { term.Clear() })
	btnClr.Importance = widget.DangerImportance

	header := container.NewStack(canvas.NewRectangle(color.Gray{Y: 45}), container.NewVBox(
		container.NewPadded(titleText),
		container.NewPadded(container.NewGridWithColumns(3, container.NewVBox(lblKernelTitle,lblKernelValue), container.NewVBox(lblSELinuxTitle,lblSELinuxValue), container.NewVBox(lblSystemTitle,lblSystemValue))),
		container.NewPadded(container.NewGridWithColumns(3, btnInj, btnSel, btnClr)),
		container.NewPadded(status),
		widget.NewSeparator(),
	))

	sendBtn := widget.NewButtonWithIcon("", theme.MailSendIcon(), send)
	bottom := container.NewVBox(container.NewPadded(createLabel("Code by TANGSAN", silverColor, 10, false)), container.NewPadded(container.NewBorder(nil, nil, nil, sendBtn, input)))
	
	bg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "bg.png", StaticContent: bgPng}); bg.FillMode = canvas.ImageFillStretch
	
	termStack := container.NewStack(canvas.NewRectangle(color.Black), bg, canvas.NewRectangle(color.RGBA{0,0,0,180}), term.scroll, gestureOverlay)

	fdImg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "fd.png", StaticContent: fdPng}); fdImg.FillMode = canvas.ImageFillContain
	fdBtn := widget.NewButton("", func() { dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) { if r!=nil { runFile(r) } }, w).Show() }); fdBtn.Importance = widget.LowImportance
	fab := container.NewVBox(layout.NewSpacer(), container.NewPadded(container.NewHBox(layout.NewSpacer(), container.NewGridWrap(fyne.NewSize(65,65), container.NewStack(container.NewPadded(fdImg), fdBtn)))), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "))

	w.SetContent(container.NewStack(container.NewBorder(header, bottom, nil, nil, termStack), fab, overlayContainer))
	w.ShowAndRun()
}

