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

// URL File Config
const ConfigURL = "https://raw.githubusercontent.com/tangsanrich/Fileku/main/executor.txt"

// KUNCI RAHASIA (32 Karakter)
const CryptoKey = "RahasiaNegaraJanganSampaiBocorir"

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
		if r := recover(); r != nil {
			fmt.Println("Recovered from decrypt panic")
		}
	}() // Anti Crash

	key := []byte(CryptoKey)
	if len(key) != 32 {
		return nil, errors.New("key length error")
	}

	encryptedStr = strings.TrimSpace(encryptedStr)

	data, err := base64.StdEncoding.DecodeString(encryptedStr)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, errors.New("data corrupt")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}

	return plaintext, nil
}

/* ==========================================
   TERMINAL LOGIC
========================================== */
type Terminal struct {
	grid     *widget.TextGrid
	scroll   *container.Scroll
	curRow   int
	curCol   int
	curStyle *widget.CustomTextGridStyle
	mutex    sync.Mutex
	reAnsi   *regexp.Regexp
}

func NewTerminal() *Terminal {
	g := widget.NewTextGrid()
	g.ShowLineNumbers = false
	defStyle := &widget.CustomTextGridStyle{
		FGColor: theme.ForegroundColor(),
		BGColor: color.Transparent,
	}
	re := regexp.MustCompile(`\x1b\[([0-9;]*)?([a-zA-Z])`)
	return &Terminal{
		grid:     g,
		scroll:   container.NewScroll(g),
		curRow:   0,
		curCol:   0,
		curStyle: defStyle,
		reAnsi:   re,
	}
}

func ansiToColor(code string) color.Color {
	switch code {
	case "30":
		return color.Gray{Y: 100}
	case "31":
		return theme.ErrorColor()
	case "32":
		return theme.SuccessColor()
	case "33":
		return theme.WarningColor()
	case "34":
		return theme.PrimaryColor()
	case "35":
		return color.RGBA{R: 200, G: 0, B: 200, A: 255}
	case "36":
		return color.RGBA{R: 0, G: 255, B: 255, A: 255}
	case "37":
		return theme.ForegroundColor()
	case "90":
		return color.Gray{Y: 100}
	case "91":
		return color.RGBA{R: 255, G: 100, B: 100, A: 255}
	case "92":
		return color.RGBA{R: 100, G: 255, B: 100, A: 255}
	case "93":
		return color.RGBA{R: 255, G: 255, B: 100, A: 255}
	case "94":
		return color.RGBA{R: 100, G: 100, B: 255, A: 255}
	case "95":
		return color.RGBA{R: 255, G: 100, B: 255, A: 255}
	case "96":
		return color.RGBA{R: 100, G: 255, B: 255, A: 255}
	case "97":
		return color.White
	default:
		return nil
	}
}

func (t *Terminal) Clear() {
	t.grid.SetText("")
	t.curRow = 0
	t.curCol = 0
}

func (t *Terminal) Write(p []byte) (int, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	raw := string(p)
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	for len(raw) > 0 {
		loc := t.reAnsi.FindStringIndex(raw)
		if loc == nil {
			t.printText(raw)
			break
		}
		if loc[0] > 0 {
			t.printText(raw[:loc[0]])
		}
		ansiCode := raw[loc[0]:loc[1]]
		t.handleAnsiCode(ansiCode)
		raw = raw[loc[1]:]
	}
	t.grid.Refresh()
	t.scroll.ScrollToBottom()
	return len(p), nil
}

func (t *Terminal) handleAnsiCode(codeSeq string) {
	if len(codeSeq) < 3 {
		return
	}
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
				if col != nil {
					t.curStyle.FGColor = col
				}
			}
		}
	case 'J':
		if strings.Contains(content, "2") {
			t.Clear()
		}
	case 'H':
		t.curRow = 0
		t.curCol = 0
	}
}

func (t *Terminal) printText(text string) {
	for _, char := range text {
		if char == '\n' {
			t.curRow++
			t.curCol = 0
			continue
		}
		if char == '\r' {
			t.curCol = 0
			continue
		}
		for t.curRow >= len(t.grid.Rows) {
			t.grid.SetRow(len(t.grid.Rows), widget.TextGridRow{Cells: []widget.TextGridCell{}})
		}
		rowCells := t.grid.Rows[t.curRow].Cells
		if t.curCol >= len(rowCells) {
			newCells := make([]widget.TextGridCell, t.curCol+1)
			copy(newCells, rowCells)
			t.grid.SetRow(t.curRow, widget.TextGridRow{Cells: newCells})
		}
		cellStyle := *t.curStyle
		t.grid.SetCell(t.curRow, t.curCol, widget.TextGridCell{
			Rune:  char,
			Style: &cellStyle,
		})
		t.curCol++
	}
}

/* ===============================
   ANIMATION & HELPERS
================================ */

func drawProgressBar(term *Terminal, label string, percent int, colorCode string) {
	barLength := 20
	filledLength := (percent * barLength) / 100
	bar := ""
	for i := 0; i < barLength; i++ {
		if i < filledLength {
			bar += "█"
		} else {
			bar += "░"
		}
	}
	msg := fmt.Sprintf("\r%s %s [%s] %d%%", colorCode, label, bar, percent)
	term.Write([]byte(msg))
}

func CheckRoot() bool {
	cmd := exec.Command("su", "-c", "id -u")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "0"
}

func CheckKernelDriver() bool {
	cmd := exec.Command("su", "-c", "ls -d /sys/module/"+TargetDriverName)
	if err := cmd.Run(); err == nil {
		return true
	}
	return false
}

func CheckSELinux() string {
	cmd := exec.Command("su", "-c", "getenforce")
	out, err := cmd.Output()
	if err != nil {
		return "Unknown"
	}
	return strings.TrimSpace(string(out))
}

func VerifySuccessAndCreateFlag() bool {
	cmd := exec.Command("su", "-c", "ls -d /sys/module/"+TargetDriverName)
	if err := cmd.Run(); err == nil {
		exec.Command("su", "-c", "touch "+FlagFile).Run()
		exec.Command("su", "-c", "chmod 777 "+FlagFile).Run()
		return true
	}
	return false
}

func downloadFile(url string, filepath string) (error, string) {
	exec.Command("su", "-c", "rm -f "+filepath).Run()

	cmdStr := fmt.Sprintf("curl -k -L -f --connect-timeout 10 -o %s %s", filepath, url)
	cmd := exec.Command("su", "-c", cmdStr)
	err := cmd.Run()

	if err == nil {
		checkCmd := exec.Command("su", "-c", "[ -s "+filepath+" ]")
		if checkCmd.Run() == nil {
			return nil, "Success"
		}
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err, "Init Fail"
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 Chrome/120.0.0.0")

	resp, err := client.Do(req)
	if err != nil {
		return err, "Net Err"
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode), "HTTP Err"
	}

	writeCmd := exec.Command("su", "-c", "cat > "+filepath)
	stdin, err := writeCmd.StdinPipe()
	if err != nil {
		return err, "Pipe Err"
	}
	go func() { defer stdin.Close(); io.Copy(stdin, resp.Body) }()

	if err := writeCmd.Run(); err != nil {
		return err, "Write Err"
	}
	return nil, "Success"
}

/* ===============================
              MAIN UI
================================ */

// Helper to create centered text labels quickly
func createLabel(text string, color color.Color, size float32, bold bool) *canvas.Text {
	lbl := canvas.NewText(text, color)
	lbl.TextSize = size
	lbl.Alignment = fyne.TextAlignCenter
	if bold {
		lbl.TextStyle = fyne.TextStyle{Bold: true}
	}
	return lbl
}

func main() {
	a := app.New()
	a.Settings().SetTheme(theme.DarkTheme())

	w := a.NewWindow("Simple Exec by TANGSAN")
	w.Resize(fyne.NewSize(400, 700))
	w.SetMaster()

	term := NewTerminal()
	brightYellow := color.RGBA{R: 255, G: 255, B: 0, A: 255}
	successGreen := color.RGBA{R: 0, G: 255, B: 0, A: 255}
	failRed := color.RGBA{R: 255, G: 50, B: 50, A: 255}
	silverColor := color.Gray{Y: 180}

	input := widget.NewEntry()
	input.SetPlaceHolder("Terminal Command...")

	status := canvas.NewText("System: Ready", silverColor)
	status.TextSize = 12
	status.Alignment = fyne.TextAlignCenter

	var stdin io.WriteCloser

	// --- Status Labels ---
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
				defer func() {
					if r := recover(); r != nil {
					}
				}()

				isRoot := CheckRoot()
				if isRoot {
					lblSystemValue.Text = "GRANTED"
					lblSystemValue.Color = successGreen
				} else {
					lblSystemValue.Text = "DENIED"
					lblSystemValue.Color = failRed
				}
				lblSystemValue.Refresh()

				if CheckKernelDriver() {
					lblKernelValue.Text = "ACTIVE"
					lblKernelValue.Color = successGreen
				} else {
					lblKernelValue.Text = "MISSING"
					lblKernelValue.Color = failRed
				}
				lblKernelValue.Refresh()

				seStatus := CheckSELinux()
				lblSELinuxValue.Text = strings.ToUpper(seStatus)
				if seStatus == "Enforcing" {
					lblSELinuxValue.Color = successGreen
				} else if seStatus == "Permissive" {
					lblSELinuxValue.Color = failRed
				} else {
					lblSELinuxValue.Color = color.Gray{Y: 150}
				}
				lblSELinuxValue.Refresh()
			}()
			time.Sleep(3 * time.Second)
		}
	}()

	// -------------------------------------------------------------
	//   ONLINE VERSION CHECKER & POPUPS
	// -------------------------------------------------------------

	var overlayContainer *fyne.Container

	showModal := func(titleText string, msgText string, confirmText string, onConfirm func(), isError bool) {
		w.Canvas().Refresh(w.Content())

		btnCancel := widget.NewButton("CANCEL", func() {
			overlayContainer.Hide()
		})
		// [MODIFIKASI] Tombol Cancel jadi Merah
		btnCancel.Importance = widget.DangerImportance

		btnConfirm := widget.NewButton(confirmText, func() {
			overlayContainer.Hide()
			if onConfirm != nil {
				onConfirm()
			}
		})

		if isError {
			btnConfirm.Importance = widget.DangerImportance
		} else {
			btnConfirm.Importance = widget.HighImportance
		}

		// [MODIFIKASI] Layout Tombol: Ukuran fix dan pakai spacer agar tidak mepet
		btnSize := fyne.NewSize(110, 40) // Ukuran tombol sedikit diperkecil agar muat
		cancelContainer := container.NewGridWrap(btnSize, btnCancel)
		confirmContainer := container.NewGridWrap(btnSize, btnConfirm)

		btnGrid := container.NewHBox(
			layout.NewSpacer(),
			cancelContainer,
			widget.NewLabel("      "), // Jarak (gap) antara tombol
			confirmContainer,
			layout.NewSpacer(),
		)

		txtColor := theme.ForegroundColor()
		if isError {
			txtColor = theme.ErrorColor()
		}

		lblTitle := canvas.NewText(titleText, txtColor)
		lblTitle.TextSize = 18
		lblTitle.TextStyle = fyne.TextStyle{Bold: true}
		lblTitle.Alignment = fyne.TextAlignCenter

		lblMsg := widget.NewLabel(msgText)
		lblMsg.Alignment = fyne.TextAlignCenter
		lblMsg.Wrapping = fyne.TextWrapWord

		content := container.NewVBox(
			container.NewPadded(container.NewCenter(lblTitle)),
			lblMsg,
			widget.NewLabel(""), // Spacer Vertical
			btnGrid,
		)

		card := widget.NewCard("", "", container.NewPadded(content))
		box := container.NewPadded(card)

		bg := canvas.NewRectangle(color.RGBA{R: 0, G: 0, B: 0, A: 220})
		modalWrapper := container.NewCenter(container.NewGridWrap(fyne.NewSize(300, 220), box))

		overlayContainer.Objects = []fyne.CanvasObject{bg, modalWrapper}
		overlayContainer.Show()
		overlayContainer.Refresh()
	}

	go func() {
		time.Sleep(1500 * time.Millisecond)
		if strings.Contains(ConfigURL, "GANTI_DENGAN_LINK") {
			term.Write([]byte("\n\x1b[33m[WARN] ConfigURL belum diganti!\x1b[0m\n"))
			return
		}

		term.Write([]byte("\n\x1b[90m[*] Checking for updates...\x1b[0m\n"))
		freshURL := fmt.Sprintf("%s?v=%d", ConfigURL, time.Now().Unix())
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(freshURL)

		if err != nil {
			term.Write([]byte("\x1b[31m[ERR] Connection Failed\x1b[0m\n"))
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			term.Write([]byte(fmt.Sprintf("\x1b[31m[ERR] Server Error: %d\x1b[0m\n", resp.StatusCode)))
			return
		}

		body, _ := io.ReadAll(resp.Body)
		body = bytes.TrimSpace(body)

		decrypted, err := decryptConfig(string(body))
		if err != nil {
			term.Write([]byte("\x1b[31m[ERR] Integrity Check Failed\x1b[0m\n"))
			return
		}

		decrypted = bytes.TrimSpace(decrypted)
		var config OnlineConfig
		if err := json.Unmarshal(decrypted, &config); err == nil {
			if config.Version != "" {
				if config.Version != AppVersion {
					term.Write([]byte("\x1b[33m[!] Update Found: " + config.Version + "\x1b[0m\n"))
					showModal("UPDATE AVAILABLE", config.Message, "UPDATE", func() {
						u, err := url.Parse(config.Link)
						if err == nil {
							app.New().OpenURL(u)
						}
					}, false)
				} else {
					term.Write([]byte("\x1b[32m[V] System Updated.\x1b[0m\n"))
				}
			}
		}
	}()

	autoInstallKernel := func() {
		term.Clear()
		status.Text = "System: Installing..."
		status.Refresh()
		go func() {
			exec.Command("su", "-c", "rm -f "+FlagFile).Run()
			term.Write([]byte("\x1b[36m╔══════════════════════════════════════╗\x1b[0m\n"))
			term.Write([]byte("\x1b[36m║      KERNEL DRIVER INSTALLER         ║\x1b[0m\n"))
			term.Write([]byte("\x1b[36m╚══════════════════════════════════════╝\x1b[0m\n"))
			term.Write([]byte("\n\x1b[90m[*] Identifying Device Architecture...\x1b[0m\n"))
			time.Sleep(500 * time.Millisecond)

			out, err := exec.Command("uname", "-r").Output()
			if err != nil {
				term.Write([]byte("\x1b[31m[X] Critical Error: Cannot read kernel.\x1b[0m\n"))
				return
			}
			rawVersion := strings.TrimSpace(string(out))
			term.Write([]byte(fmt.Sprintf(" -> Target: \x1b[33m%s\x1b[0m\n\n", rawVersion)))

			downloadPath := "/data/local/tmp/temp_kernel_dl"
			targetFile := "/data/local/tmp/kernel_installer.sh"
			var downloadUrl string
			var found bool = false

			simulateProcess := func(label string) {
				for i := 0; i <= 100; i += 10 {
					drawProgressBar(term, label, i, "\x1b[36m")
					time.Sleep(50 * time.Millisecond)
				}
				term.Write([]byte("\n"))
			}

			term.Write([]byte("\x1b[97m[*] Checking Repository (Variant 1)...\x1b[0m\n"))
			simulateProcess("Connecting...")
			url1 := GitHubRepo + rawVersion + ".sh"
			err, _ = downloadFile(url1, downloadPath)
			if err == nil {
				downloadUrl = "Variant 1 (Precise)"
				found = true
				term.Write([]byte("\x1b[32m[V] Resources Found.\x1b[0m\n"))
			}

			if !found {
				parts := strings.Split(rawVersion, "-")
				if len(parts) > 0 {
					term.Write([]byte("\n\x1b[97m[*] Checking Repository (Variant 2)...\x1b[0m\n"))
					simulateProcess("Connecting...")
					shortVersion := parts[0]
					url2 := GitHubRepo + shortVersion + ".sh"
					err, _ = downloadFile(url2, downloadPath)
					if err == nil {
						downloadUrl = "Variant 2 (Universal)"
						found = true
						term.Write([]byte("\x1b[32m[V] Resources Found.\x1b[0m\n"))
					}
				}
			}

			if !found {
				term.Write([]byte("\n\x1b[31m[DRIVER NOT FOUND]\x1b[0m\n"))
				status.Text = "System: Failed"
				status.Refresh()
			} else {
				term.Write([]byte("\n\x1b[92m[*] Downloading Script: " + downloadUrl + "\x1b[0m\n"))
				simulateProcess("Downloading Payload")
				exec.Command("su", "-c", "mv "+downloadPath+" "+targetFile).Run()
				exec.Command("su", "-c", "chmod 777 "+targetFile).Run()
				cmd := exec.Command("su", "-c", "sh "+targetFile)
				cmd.Env = append(os.Environ(), "TERM=xterm-256color")
				var pipeStdin io.WriteCloser
				pipeStdin, _ = cmd.StdinPipe()
				cmd.Stdout = term
				cmd.Stderr = term
				cmd.Run()
				VerifySuccessAndCreateFlag()
				pipeStdin.Close()
				time.Sleep(1 * time.Second)
				status.Text = "System: Online"
				status.Refresh()
			}
		}()
	}

	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		term.Clear()
		status.Text = "Status: Processing..."
		status.Refresh()

		data, err := io.ReadAll(reader)
		if err != nil {
			term.Write([]byte("\x1b[31m[ERR] Read Failed\x1b[0m\n"))
			return
		}

		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))
		target := "/data/local/tmp/temp_exec"

		go func() {
			tmpFile, err := os.CreateTemp("", "exec_tmp")
			if err != nil {
				term.Write([]byte("\x1b[31m[ERR] Cache Write Failed\x1b[0m\n"))
				return
			}
			tmpFile.Write(data)
			tmpPath := tmpFile.Name()
			tmpFile.Close()

			exec.Command("su", "-c", "rm -f "+target).Run()

			moveCmd := exec.Command("su", "-c", fmt.Sprintf("cp %s %s && chmod 777 %s", tmpPath, target, target))
			if err := moveCmd.Run(); err != nil {
				term.Write([]byte("\x1b[31m[ERR] Copy Failed (Check Root)\x1b[0m\n"))
				os.Remove(tmpPath)
				return
			}
			os.Remove(tmpPath)

			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", target)
			} else {
				cmd = exec.Command("su", "-c", "sh "+target)
			}

			cmd.Env = append(os.Environ(), "TERM=xterm-256color")

			stdout, _ := cmd.StdoutPipe()
			stderr, _ := cmd.StderrPipe()
			stdin, _ = cmd.StdinPipe()

			if err := cmd.Start(); err != nil {
				term.Write([]byte("\x1b[31m[ERR] Execution Failed\x1b[0m\n"))
				return
			}

			go io.Copy(term, stdout)
			go io.Copy(term, stderr)

			cmd.Wait()
			status.Text = "Status: Idle"
			status.Refresh()
			stdin = nil
		}()
	}

	send := func() {
		if stdin != nil && input.Text != "" {
			fmt.Fprintln(stdin, input.Text)
			term.Write([]byte(fmt.Sprintf("\x1b[36m> %s\x1b[0m\n", input.Text)))
			input.SetText("")
		}
	}
	input.OnSubmitted = func(_ string) { send() }

	// --- UI COMPOSITION (New Layout) ---

	// 1. HEADER
	titleText := canvas.NewText("SIMPLE EXEC", color.White)
	titleText.TextSize = 16
	titleText.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
	titleText.Alignment = fyne.TextAlignCenter

	infoGrid := container.NewGridWithColumns(3,
		container.NewVBox(lblKernelTitle, lblKernelValue),
		container.NewVBox(lblSELinuxTitle, lblSELinuxValue),
		container.NewVBox(lblSystemTitle, lblSystemValue),
	)

	// 2. BUTTONS
	btnInject := widget.NewButtonWithIcon("Inject", theme.DownloadIcon(), func() {
		showModal("INJECT DRIVER", "Start automatic injection process?", "START", autoInstallKernel, false)
	})
	btnInject.Importance = widget.HighImportance

	btnSwitch := widget.NewButtonWithIcon("SELinux", theme.ViewRefreshIcon(), func() {
		go func() {
			current := CheckSELinux()
			if current == "Enforcing" {
				exec.Command("su", "-c", "setenforce 0").Run()
			} else {
				exec.Command("su", "-c", "setenforce 1").Run()
			}
		}()
	})
	btnSwitch.Importance = widget.HighImportance

	btnClear := widget.NewButtonWithIcon("Clear", theme.ContentClearIcon(), func() { term.Clear() })
	btnClear.Importance = widget.DangerImportance

	actionGrid := container.NewGridWithColumns(3, btnInject, btnSwitch, btnClear)

	headerContainer := container.NewVBox(
		container.NewPadded(titleText),
		container.NewPadded(infoGrid),
		container.NewPadded(actionGrid),
		container.NewPadded(status),
		widget.NewSeparator(),
	)

	headerBg := canvas.NewRectangle(color.Gray{Y: 45})
	headerStack := container.NewStack(headerBg, headerContainer)

	// 3. BOTTOM SECTION
	sendBtn := widget.NewButtonWithIcon("", theme.MailSendIcon(), send)
	inputArea := container.NewBorder(nil, nil, nil, sendBtn, input)

	copyrightLbl := canvas.NewText("Code by TANGSAN", silverColor)
	copyrightLbl.TextSize = 10
	copyrightLbl.Alignment = fyne.TextAlignCenter
	copyrightContainer := container.NewPadded(copyrightLbl)

	bottomContainer := container.NewVBox(
		copyrightContainer,
		container.NewPadded(inputArea),
	)

	// 4. MAIN TERMINAL AREA
	bgImg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "bg.png", StaticContent: bgPng})
	bgImg.FillMode = canvas.ImageFillStretch

	darkOverlay := canvas.NewRectangle(color.RGBA{R: 0, G: 0, B: 0, A: 180})

	termArea := container.NewStack(
		canvas.NewRectangle(color.Black),
		bgImg,
		darkOverlay,
		term.scroll,
	)

	// 5. FAB
	img := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "fd.png", StaticContent: fdPng})
	img.FillMode = canvas.ImageFillContain

	fabButton := widget.NewButton("", func() {
		dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) { if r != nil { runFile(r) } }, w).Show()
	})
	fabButton.Importance = widget.LowImportance

	fabContent := container.NewStack(
		container.NewPadded(img),
		fabButton,
	)

	fabWrapper := container.NewHBox(
		layout.NewSpacer(),
		container.NewGridWrap(fyne.NewSize(65, 65), fabContent),
	)
	fabContainer := container.NewVBox(
		layout.NewSpacer(),
		container.NewPadded(fabWrapper),
		widget.NewLabel(" "),
		widget.NewLabel(" "),
	)

	// --- FINAL ASSEMBLY ---
	mainLayout := container.NewBorder(headerStack, bottomContainer, nil, nil, termArea)
	overlayContainer = container.NewStack()
	overlayContainer.Hide()

	finalContent := container.NewStack(mainLayout, fabContainer, overlayContainer)

	w.SetContent(finalContent)
	w.ShowAndRun()
}
