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
   CONFIG & SECURITY
========================================== */
const AppVersion = "1.0"
const GitHubRepo = "https://raw.githubusercontent.com/richstoremipad/My-executor/main/Driver/"
const FlagFile = "/dev/status_driver_aktif" 
const TargetDriverName = "5.10_A12"

// Link RAW Pastebin (Isinya HARUS string terenkripsi dari generator)
const ConfigURL = "https://docs.google.com/document/d/1D1J3Vg21ftUaZPLOiVgOAN-mysy7P3L55IE5aNfU_OE/export?format=txt" 

// KUNCI HARUS SAMA PERSIS DENGAN GENERATOR (32 Karakter)
const CryptoKey = "hwjXl21nLPDhCHdnzy2rhTi9GDOCixmYHtfyR74SXC7ZQXAok9hg2kDWUebIvvLXvIFfsK+9MVOy/vxW3Z5RGPnj/RnmVLNaAXLNOXnkOBrrTY3xGrB4e805C8I1zH3H9Ept7ffbQKng86bBO8tG7n2WGg9J4XpMOWu94des7BB6Wmj4bOY="

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
   SECURITY LOGIC (AES DECRYPTION)
========================================== */

func decryptConfig(encryptedStr string) ([]byte, error) {
	key := []byte(CryptoKey)
	data, err := base64.StdEncoding.DecodeString(encryptedStr)
	if err != nil { return nil, err }

	block, err := aes.NewCipher(key)
	if err != nil { return nil, err }

	gcm, err := cipher.NewGCM(block)
	if err != nil { return nil, err }

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize { return nil, errors.New("invalid data") }

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil { return nil, err }

	return plaintext, nil
}

/* ==========================================
   TERMINAL LOGIC (TIDAK BERUBAH)
========================================== */
// ... (Bagian Terminal sama seperti sebelumnya, saya persingkat agar muat) ...
type Terminal struct {
	grid     *widget.TextGrid
	scroll   *container.Scroll
	curRow   int; curCol   int; curStyle *widget.CustomTextGridStyle
	mutex    sync.Mutex; reAnsi   *regexp.Regexp
}
func NewTerminal() *Terminal {
	g := widget.NewTextGrid(); g.ShowLineNumbers = false
	defStyle := &widget.CustomTextGridStyle{FGColor: theme.ForegroundColor(), BGColor: color.Transparent}
	re := regexp.MustCompile(`\x1b\[([0-9;]*)?([a-zA-Z])`)
	return &Terminal{grid: g, scroll: container.NewScroll(g), curStyle: defStyle, reAnsi: re}
}
func (t *Terminal) Clear() { t.grid.SetText(""); t.curRow = 0; t.curCol = 0 }
func ansiToColor(code string) color.Color {
	switch code {
	case "31": return theme.ErrorColor(); case "32": return theme.SuccessColor()
	case "33": return theme.WarningColor(); case "36": return color.RGBA{R: 0, G: 255, B: 255, A: 255}
	case "90": return color.Gray{Y: 100}; default: return theme.ForegroundColor()
	}
}
func (t *Terminal) Write(p []byte) (int, error) {
	t.mutex.Lock(); defer t.mutex.Unlock()
	raw := string(p); raw = strings.ReplaceAll(raw, "\r\n", "\n")
	for len(raw) > 0 {
		loc := t.reAnsi.FindStringIndex(raw)
		if loc == nil { t.printText(raw); break }
		if loc[0] > 0 { t.printText(raw[:loc[0]]) }
		t.handleAnsiCode(raw[loc[0]:loc[1]]); raw = raw[loc[1]:]
	}
	t.grid.Refresh(); t.scroll.ScrollToBottom(); return len(p), nil
}
func (t *Terminal) handleAnsiCode(codeSeq string) {
	if len(codeSeq) < 3 { return }
	content := codeSeq[2 : len(codeSeq)-1]; parts := strings.Split(content, ";")
	for _, part := range parts {
		if part == "" || part == "0" { t.curStyle.FGColor = theme.ForegroundColor() } else {
			col := ansiToColor(part); if col != nil { t.curStyle.FGColor = col }
		}
	}
	if strings.Contains(content, "2") && codeSeq[len(codeSeq)-1] == 'J' { t.Clear() }
}
func (t *Terminal) printText(text string) {
	for _, char := range text {
		if char == '\n' { t.curRow++; t.curCol = 0; continue }
		if char == '\r' { t.curCol = 0; continue }
		for t.curRow >= len(t.grid.Rows) { t.grid.SetRow(len(t.grid.Rows), widget.TextGridRow{}) }
		rowCells := t.grid.Rows[t.curRow].Cells
		if t.curCol >= len(rowCells) {
			newCells := make([]widget.TextGridCell, t.curCol+1); copy(newCells, rowCells)
			t.grid.SetRow(t.curRow, widget.TextGridRow{Cells: newCells})
		}
		s := *t.curStyle; t.grid.SetCell(t.curRow, t.curCol, widget.TextGridCell{Rune: char, Style: &s}); t.curCol++
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
func CheckKernelDriver() bool { return exec.Command("su", "-c", "ls -d /sys/module/"+TargetDriverName).Run() == nil }
func CheckSELinux() string { out, _ := exec.Command("su", "-c", "getenforce").Output(); return strings.TrimSpace(string(out)) }
func CheckRoot() bool { out, _ := exec.Command("su", "-c", "id -u").Output(); return strings.TrimSpace(string(out)) == "0" }
func VerifySuccessAndCreateFlag() { exec.Command("su", "-c", "touch "+FlagFile+" && chmod 777 "+FlagFile).Run() }

func downloadFile(url string, filepath string) (error, string) {
	exec.Command("su", "-c", "rm -f "+filepath).Run()
	if exec.Command("su", "-c", fmt.Sprintf("curl -k -L -f --connect-timeout 10 -o %s %s", filepath, url)).Run() == nil {
		if exec.Command("su", "-c", "[ -s "+filepath+" ]").Run() == nil { return nil, "Success" }
	}
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", url, nil); req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req); if err != nil { return err, "Net Err" }
	defer resp.Body.Close()
	if resp.StatusCode != 200 { return fmt.Errorf("HTTP %d", resp.StatusCode), "HTTP Err" }
	writeCmd := exec.Command("su", "-c", "cat > "+filepath); stdin, _ := writeCmd.StdinPipe()
	go func() { defer stdin.Close(); io.Copy(stdin, resp.Body) }(); 
	return writeCmd.Run(), "Success"
}

/* ===============================
              MAIN UI
================================ */
func main() {
	a := app.New(); a.Settings().SetTheme(theme.DarkTheme())
	w := a.NewWindow("Simple Exec by TANGSAN"); w.Resize(fyne.NewSize(720, 520)); w.SetMaster()

	term := NewTerminal()
	brightYellow := color.RGBA{R: 255, G: 255, B: 0, A: 255}
	successGreen := color.RGBA{R: 0, G: 255, B: 0, A: 255}
	failRed := color.RGBA{R: 255, G: 50, B: 50, A: 255}
	grayHeaderColor := color.Gray{Y: 60}

	input := widget.NewEntry(); input.SetPlaceHolder("Terminal Command...")
	status := widget.NewLabel("System: Ready"); status.TextStyle = fyne.TextStyle{Bold: true}
	var stdin io.WriteCloser

	// Monitor Labels
	lblKernelTitle := canvas.NewText("KERNEL: ", brightYellow); lblKernelTitle.TextSize = 10; lblKernelTitle.TextStyle = fyne.TextStyle{Bold: true}
	lblKernelValue := canvas.NewText("CHECKING...", color.Gray{Y: 150}); lblKernelValue.TextSize = 10; lblKernelValue.TextStyle = fyne.TextStyle{Bold: true}
	lblSELinuxTitle := canvas.NewText("SELINUX: ", brightYellow); lblSELinuxTitle.TextSize = 10; lblSELinuxTitle.TextStyle = fyne.TextStyle{Bold: true}
	lblSELinuxValue := canvas.NewText("CHECKING...", color.Gray{Y: 150}); lblSELinuxValue.TextSize = 10; lblSELinuxValue.TextStyle = fyne.TextStyle{Bold: true}
	lblSystemTitle := canvas.NewText("SYSTEM: ", brightYellow); lblSystemTitle.TextSize = 10; lblSystemTitle.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
	lblSystemValue := canvas.NewText("CHECKING ROOT...", color.Gray{Y: 150}); lblSystemValue.TextSize = 10; lblSystemValue.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}

	// Realtime Monitor
	go func() {
		for {
			if CheckRoot() { lblSystemValue.Text = "ROOT ACCESS GRANTED"; lblSystemValue.Color = successGreen } else { lblSystemValue.Text = "ROOT ACCESS DENIED"; lblSystemValue.Color = failRed }; lblSystemValue.Refresh()
			if CheckKernelDriver() { lblKernelValue.Text = "DETECTED"; lblKernelValue.Color = successGreen } else { lblKernelValue.Text = "NOT FOUND"; lblKernelValue.Color = failRed }; lblKernelValue.Refresh()
			se := CheckSELinux(); lblSELinuxValue.Text = strings.ToUpper(se)
			if se == "Enforcing" { lblSELinuxValue.Color = successGreen } else if se == "Permissive" { lblSELinuxValue.Color = failRed } else { lblSELinuxValue.Color = color.Gray{Y: 150} }; lblSELinuxValue.Refresh()
			time.Sleep(3 * time.Second)
		}
	}()

	// --- LOGIC INSTALL & RUN ---
	autoInstallKernel := func() {
		term.Clear(); status.SetText("System: Installing...")
		go func() {
			exec.Command("su", "-c", "rm -f "+FlagFile).Run()
			term.Write([]byte("\x1b[36m╔══════════════════════════════════════╗\x1b[0m\n"))
			term.Write([]byte("\x1b[36m║      KERNEL DRIVER INSTALLER         ║\x1b[0m\n"))
			term.Write([]byte("\x1b[36m╚══════════════════════════════════════╝\x1b[0m\n"))
			term.Write([]byte("\n\x1b[90m[*] Identifying Device Architecture...\x1b[0m\n")); time.Sleep(500 * time.Millisecond)
			
			out, _ := exec.Command("uname", "-r").Output(); rawVersion := strings.TrimSpace(string(out))
			term.Write([]byte(fmt.Sprintf(" -> Target: \x1b[33m%s\x1b[0m\n\n", rawVersion)))
			
			path := "/data/local/tmp/temp_kernel_dl"; target := "/data/local/tmp/kernel_installer.sh"
			
			sim := func(l string) { for i:=0; i<=100; i+=10 { drawProgressBar(term, l, i, "\x1b[36m"); time.Sleep(30 * time.Millisecond) }; term.Write([]byte("\n")) }
			
			term.Write([]byte("\x1b[97m[*] Checking Repository...\x1b[0m\n")); sim("Connecting...")
			
			url := GitHubRepo + rawVersion + ".sh"; found := false
			if _, msg := downloadFile(url, path); msg == "Success" { found = true }
			
			if !found {
				parts := strings.Split(rawVersion, "-"); 
				if len(parts) > 0 {
					url = GitHubRepo + parts[0] + ".sh"
					if _, msg := downloadFile(url, path); msg == "Success" { found = true }
				}
			}

			if !found {
				term.Write([]byte("\n\x1b[31m[DRIVER NOT FOUND]\x1b[0m\n")); status.SetText("System: Failed")
			} else {
				term.Write([]byte("\n\x1b[92m[*] Downloading Payload...\x1b[0m\n")); sim("Downloading")
				exec.Command("su", "-c", "mv "+path+" "+target+" && chmod 777 "+target).Run()
				cmd := exec.Command("su", "-c", "sh "+target); cmd.Env = append(os.Environ(), "TERM=xterm-256color")
				pipe, _ := cmd.StdinPipe(); cmd.Stdout = term; cmd.Stderr = term; cmd.Run()
				VerifySuccessAndCreateFlag(); pipe.Close(); time.Sleep(1 * time.Second); status.SetText("System: Online")
			}
		}()
	}

	runFile := func(r fyne.URIReadCloser) {
		defer r.Close(); term.Clear(); status.SetText("Status: Processing...")
		data, _ := io.ReadAll(r); target := "/data/local/tmp/temp_exec"
		isBin := bytes.HasPrefix(data, []byte("\x7fELF"))
		go func() {
			exec.Command("su", "-c", "rm -f "+target).Run()
			cp := exec.Command("su", "-c", "cat > "+target+" && chmod 777 "+target); in, _ := cp.StdinPipe()
			go func() { defer in.Close(); in.Write(data) }(); cp.Run()
			
			var cmd *exec.Cmd; if isBin { cmd = exec.Command("su", "-c", target) } else { cmd = exec.Command("su", "-c", "sh "+target) }
			cmd.Env = append(os.Environ(), "TERM=xterm-256color"); stdin, _ = cmd.StdinPipe()
			cmd.Stdout = term; cmd.Stderr = term; cmd.Run(); status.SetText("Status: Idle"); stdin = nil
		}()
	}

	send := func() { if stdin != nil && input.Text != "" { fmt.Fprintln(stdin, input.Text); term.Write([]byte(fmt.Sprintf("\x1b[36m> %s\x1b[0m\n", input.Text))); input.SetText("") } }
	input.OnSubmitted = func(_ string) { send() }

	// --- POPUP SYSTEM ---
	var updateOverlay *fyne.Container
	var popupOverlay *fyne.Container

	showPopup := func(title, msg string, isError bool, link string) {
		w.Canvas().Refresh(w.Content())
		
		btnExit := widget.NewButton(func() string { if isError { return "EXIT" } else { return "CANCEL" } }(), func() { os.Exit(0) })
		btnExit.Importance = widget.DangerImportance
		
		var btns *fyne.Container
		if isError {
			btns = container.NewCenter(container.NewGridWrap(fyne.NewSize(140, 40), btnExit))
		} else {
			btnUpdate := widget.NewButton("UPDATE", func() { u, _ := url.Parse(link); app.New().OpenURL(u) })
			btnUpdate.Importance = widget.HighImportance
			btns = container.NewHBox(layout.NewSpacer(), container.NewGridWrap(fyne.NewSize(140, 40), btnExit), widget.NewLabel("        "), container.NewGridWrap(fyne.NewSize(140, 40), btnUpdate), layout.NewSpacer())
		}

		tColor := theme.WarningColor(); if isError { tColor = theme.ErrorColor() }
		lblTitle := canvas.NewText(title, tColor); lblTitle.TextSize = 20; lblTitle.TextStyle = fyne.TextStyle{Bold: true}; lblTitle.Alignment = fyne.TextAlignCenter
		lblMsg := widget.NewLabel(msg); lblMsg.Alignment = fyne.TextAlignCenter; lblMsg.Wrapping = fyne.TextWrapWord

		content := container.NewVBox(widget.NewLabel(" "), container.NewCenter(lblTitle), widget.NewLabel(" "), lblMsg, layout.NewSpacer(), btns, widget.NewLabel(" "))
		bg := canvas.NewRectangle(color.RGBA{R: 0, G: 0, B: 0, A: 240})
		card := widget.NewCard("", "", container.NewPadded(content))
		
		updateOverlay.Objects = []fyne.CanvasObject{bg, container.NewCenter(container.NewGridWrap(fyne.NewSize(550, 240), card))}
		updateOverlay.Show(); updateOverlay.Refresh()
	}

	// --- SECURE UPDATE CHECKER ---
	go func() {
		time.Sleep(2 * time.Second)
		if strings.Contains(ConfigURL, "LINK_ENCRYPTED") { term.Write([]byte("\n\x1b[33m[WARN] ConfigURL belum diisi!\x1b[0m\n")); return }
		term.Write([]byte("\n\x1b[90m[*] Checking updates...\x1b[0m\n"))

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(ConfigURL)
		if err != nil { 
			term.Write([]byte("\x1b[31m[ERR] Connection Failed\x1b[0m\n")) // URL Hidden from logs
			showPopup("CONNECTION ERROR", "Unable to secure connection to server.\nPlease check your internet.", true, "")
			return 
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			term.Write([]byte("\x1b[31m[ERR] Server Unreachable\x1b[0m\n"))
			showPopup("SERVER ERROR", "Maintenance in progress.\nPlease try again later.", true, "")
			return
		}

		body, _ := io.ReadAll(resp.Body)
		body = bytes.TrimSpace(body)
		
		// Decrypt Payload
		decrypted, err := decryptConfig(string(body))
		if err != nil {
			term.Write([]byte("\x1b[31m[ERR] Integrity Check Failed\x1b[0m\n")) // Encryption error means tampering
			showPopup("SECURITY ALERT", "Data integrity verification failed.\nApp modified or connection insecure.", true, "")
			return
		}

		var config OnlineConfig
		if err := json.Unmarshal(decrypted, &config); err == nil && config.Version != "" {
			if config.Version != AppVersion {
				term.Write([]byte("\x1b[33m[!] New Version: " + config.Version + "\x1b[0m\n"))
				showPopup("UPDATE REQUIRED", config.Message, false, config.Link)
			} else {
				term.Write([]byte("\x1b[32m[V] System Up-to-date\x1b[0m\n"))
			}
		}
	}()

	// --- UI COMPOSITION ---
	
	// Inject Popup
	popupBtnNo := widget.NewButton("NO", func() { popupOverlay.Hide() }); popupBtnNo.Importance = widget.DangerImportance
	popupBtnYes := widget.NewButton("YES", func() { popupOverlay.Hide(); autoInstallKernel() }); popupBtnYes.Importance = widget.HighImportance
	popupBtns := container.NewHBox(layout.NewSpacer(), container.NewGridWrap(fyne.NewSize(140, 40), popupBtnNo), widget.NewLabel("        "), container.NewGridWrap(fyne.NewSize(140, 40), popupBtnYes), layout.NewSpacer())
	
	pTitle := canvas.NewText("Inject Driver", theme.ForegroundColor()); pTitle.TextSize = 20; pTitle.TextStyle = fyne.TextStyle{Bold: true}; pTitle.Alignment = fyne.TextAlignCenter
	pContent := container.NewVBox(widget.NewLabel(" "), container.NewCenter(pTitle), widget.NewLabel(" "), widget.NewLabel("Start automatic injection process?"), layout.NewSpacer(), popupBtns, widget.NewLabel(" "))
	
	popupOverlay = container.NewStack(canvas.NewRectangle(color.RGBA{R: 0, G: 0, B: 0, A: 200}), container.NewCenter(container.NewGridWrap(fyne.NewSize(550, 240), widget.NewCard("", "", container.NewPadded(pContent)))))
	popupOverlay.Hide()

	// Header Buttons
	btnSz := fyne.NewSize(130, 40)
	btnSel := widget.NewButtonWithIcon("SELinux Switch", theme.ViewRefreshIcon(), func() { s:=CheckSELinux(); v:="0"; if s!="Enforcing"{v="1"}; exec.Command("su","-c","setenforce "+v).Run() }); btnSel.Importance = widget.MediumImportance
	btnInj := widget.NewButtonWithIcon("Inject Driver", theme.DownloadIcon(), func() { popupOverlay.Show() }); btnInj.Importance = widget.MediumImportance
	
	btnClr := widget.NewButtonWithIcon("Clear Log", theme.ContentClearIcon(), func() { term.Clear() }); btnClr.Importance = widget.LowImportance
	bgClr := canvas.NewRectangle(color.RGBA{R: 200, G: 0, B: 0, A: 100}); bgClr.CornerRadius = theme.InputRadiusSize()
	
	headerRight := container.NewHBox(container.NewGridWrap(btnSz, btnInj), widget.NewLabel(" "), container.NewGridWrap(btnSz, btnSel), widget.NewLabel(" "), container.NewGridWrap(btnSz, container.NewStack(bgClr, btnClr)))
	headerBar := container.NewStack(canvas.NewRectangle(grayHeaderColor), container.NewPadded(container.NewBorder(nil, nil, container.NewPadded(container.NewVBox(titleText, container.NewHBox(lblKernelTitle, lblKernelValue), container.NewHBox(lblSELinuxTitle, lblSELinuxValue))), headerRight)))

	// Main Layout
	updateOverlay = container.NewStack(); updateOverlay.Hide()
	
	bgI := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "bg.png", StaticContent: bgPng}); bgI.FillMode = canvas.ImageFillStretch
	
	top := container.NewVBox(headerBar, container.NewPadded(status), widget.NewSeparator())
	bottom := container.NewVBox(container.NewHBox(layout.NewSpacer(), lblSystemTitle, lblSystemValue, layout.NewSpacer()), container.NewPadded(container.NewPadded(container.NewBorder(nil, nil, nil, container.NewHBox(widget.NewLabel("   "), container.NewGridWrap(fyne.NewSize(120, 60), widget.NewButtonWithIcon("Kirim", theme.MailSendIcon(), send))), container.NewPadded(input)))))
	
	fdI := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "fd.png", StaticContent: fdPng}); fdI.FillMode = canvas.ImageFillContain
	fab := container.NewStack(container.NewGridWrap(fyne.NewSize(60, 60), fdI), widget.NewButton("", func() { dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) { if r!=nil { runFile(r) } }, w).Show() }))
	fab.Objects[1].(*widget.Button).Importance = widget.LowImportance
	fabPos := container.NewVBox(layout.NewSpacer(), container.NewHBox(layout.NewSpacer(), fab, widget.NewLabel(" ")), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "))

	w.SetContent(container.NewStack(container.NewBorder(top, bottom, nil, nil, container.NewStack(bgI, term.scroll)), fabPos, popupOverlay, updateOverlay))
	w.ShowAndRun()
}

