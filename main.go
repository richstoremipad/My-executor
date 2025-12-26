package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"image/color"
	"io"
	"net/http"
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
   CONFIG
========================================== */
const GitHubRepo = "https://raw.githubusercontent.com/richstoremipad/My-executor/main/Driver/"
const FlagFile = "/dev/status_driver_aktif" 
const TargetDriverName = "5.10_A12"

//go:embed fd.png
var fdPng []byte

// PASTIKAN ada file 'bg.png' di folder yang sama
//go:embed bg.png
var bgPng []byte

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

// Cek Kernel Valid (Cek folder modul langsung)
func CheckKernelDriver() bool {
	cmd := exec.Command("su", "-c", "ls -d /sys/module/"+TargetDriverName)
	if err := cmd.Run(); err == nil { return true }
	return false 
}

// Cek SELinux Valid
func CheckSELinux() string {
	cmd := exec.Command("su", "-c", "getenforce")
	out, err := cmd.Output()
	if err != nil { return "Unknown" }
	return strings.TrimSpace(string(out))
}

// Cek Root Valid
func CheckRoot() bool {
	cmd := exec.Command("su", "-c", "id -u")
	out, err := cmd.Output()
	if err != nil { return false }
	// Jika output 0 berarti root
	return strings.TrimSpace(string(out)) == "0"
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

/* ===============================
              MAIN UI
================================ */

func main() {
	a := app.New()
	a.Settings().SetTheme(theme.DarkTheme())

	w := a.NewWindow("Simple Exec by TANGSAN")
	w.Resize(fyne.NewSize(720, 520))
	w.SetMaster()

	term := NewTerminal()
	brightYellow := color.RGBA{R: 255, G: 255, B: 0, A: 255}
	successGreen := color.RGBA{R: 0, G: 255, B: 0, A: 255}
	failRed := color.RGBA{R: 255, G: 50, B: 50, A: 255}

	input := widget.NewEntry()
	input.SetPlaceHolder("Terminal Command...")

	status := widget.NewLabel("System: Ready")
	status.TextStyle = fyne.TextStyle{Bold: true}
	var stdin io.WriteCloser

	// [WARNA HEADER - ABU ABU]
	grayHeaderColor := color.Gray{Y: 60}

	lblKernelTitle := canvas.NewText("KERNEL: ", brightYellow)
	lblKernelTitle.TextSize = 10; lblKernelTitle.TextStyle = fyne.TextStyle{Bold: true}
	lblKernelValue := canvas.NewText("CHECKING...", color.Gray{Y: 150})
	lblKernelValue.TextSize = 10; lblKernelValue.TextStyle = fyne.TextStyle{Bold: true}

	lblSELinuxTitle := canvas.NewText("SELINUX: ", brightYellow)
	lblSELinuxTitle.TextSize = 10; lblSELinuxTitle.TextStyle = fyne.TextStyle{Bold: true}
	lblSELinuxValue := canvas.NewText("CHECKING...", color.Gray{Y: 150})
	lblSELinuxValue.TextSize = 10; lblSELinuxValue.TextStyle = fyne.TextStyle{Bold: true}

	// [LABEL SYSTEM (ROOT)]
	lblSystemTitle := canvas.NewText("SYSTEM: ", brightYellow)
	lblSystemTitle.TextSize = 10; lblSystemTitle.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
	lblSystemValue := canvas.NewText("CHECKING ROOT...", color.Gray{Y: 150})
	lblSystemValue.TextSize = 10; lblSystemValue.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}

	// [REAL TIME MONITORING LOOP]
	go func() {
		for {
			// 1. Cek Root
			isRoot := CheckRoot()
			if isRoot {
				lblSystemValue.Text = "ROOT ACCESS GRANTED"
				lblSystemValue.Color = successGreen
			} else {
				lblSystemValue.Text = "ROOT ACCESS DENIED"
				lblSystemValue.Color = failRed
			}
			lblSystemValue.Refresh()

			// 2. Cek Kernel Driver
			if CheckKernelDriver() {
				lblKernelValue.Text = "DETECTED"
				lblKernelValue.Color = successGreen
			} else {
				lblKernelValue.Text = "NOT FOUND"
				lblKernelValue.Color = failRed
			}
			lblKernelValue.Refresh()

			// 3. Cek SELinux
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

			time.Sleep(3 * time.Second)
		}
	}()

	autoInstallKernel := func() {
		term.Clear()
		status.SetText("System: Installing...")
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
				for i := 0; i <= 100; i+=10 {
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
				status.SetText("System: Failed")
			} else {
				term.Write([]byte("\n\x1b[92m[*] Downloading Script: " + downloadUrl + "\x1b[0m\n"))
				simulateProcess("Downloading Payload")
				exec.Command("su", "-c", "mv "+downloadPath+" "+targetFile).Run()
				exec.Command("su", "-c", "chmod 777 "+targetFile).Run()
				cmd := exec.Command("su", "-c", "sh "+targetFile)
				cmd.Env = append(os.Environ(), "TERM=xterm-256color")
				var pipeStdin io.WriteCloser
				pipeStdin, _ = cmd.StdinPipe()
				cmd.Stdout = term; cmd.Stderr = term
				cmd.Run()
				VerifySuccessAndCreateFlag()
				pipeStdin.Close()
				time.Sleep(1 * time.Second)
				status.SetText("System: Online")
			}
		}()
	}

	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		term.Clear()
		status.SetText("Status: Processing...")
		data, _ := io.ReadAll(reader)
		target := "/data/local/tmp/temp_exec"
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))
		go func() {
			exec.Command("su", "-c", "rm -f "+target).Run()
			copyCmd := exec.Command("su", "-c", "cat > "+target+" && chmod 777 "+target)
			in, _ := copyCmd.StdinPipe()
			go func() { defer in.Close(); in.Write(data) }()
			copyCmd.Run()
			var cmd *exec.Cmd
			if isBinary { cmd = exec.Command("su", "-c", target)
			} else { cmd = exec.Command("su", "-c", "sh "+target) }
			cmd.Env = append(os.Environ(), "TERM=xterm-256color")
			stdin, _ = cmd.StdinPipe()
			cmd.Stdout = term; cmd.Stderr = term
			cmd.Run()
			status.SetText("Status: Idle")
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
	
	input.OnSubmitted = func(_ string) { 
		send() 
	}

	// [LAYOUT HEADER]
	titleText := canvas.NewText("Simple Exec by TANGSAN", theme.ForegroundColor())
	titleText.TextSize = 16; titleText.TextStyle = fyne.TextStyle{Bold: true}

	headerLeft := container.NewVBox(
		titleText,
		container.NewHBox(lblKernelTitle, lblKernelValue),
		container.NewHBox(lblSELinuxTitle, lblSELinuxValue),
	)

	const btnWidth = 130
	const btnHeight = 40
	btnSize := fyne.NewSize(btnWidth, btnHeight)

	// SELinux Button
	selinuxBtn := widget.NewButtonWithIcon("SELinux Switch", theme.ViewRefreshIcon(), func() {
		go func() {
			current := CheckSELinux()
			if current == "Enforcing" { exec.Command("su", "-c", "setenforce 0").Run()
			} else { exec.Command("su", "-c", "setenforce 1").Run() }
		}()
	})
	selinuxBtn.Importance = widget.MediumImportance

	// Clear Button
	realClearBtn := widget.NewButtonWithIcon("Clear Log", theme.ContentClearIcon(), func() { term.Clear() })
	realClearBtn.Importance = widget.LowImportance 
	clearBg := canvas.NewRectangle(color.RGBA{R: 200, G: 0, B: 0, A: 100})
	clearBg.CornerRadius = theme.InputRadiusSize()
	clearStack := container.NewStack(clearBg, realClearBtn)

	// -------------------------------------------------------------
	//   CUSTOM POPUP "INJECT DRIVER" - [LAYOUT BUTTON DIPERBAIKI]
	// -------------------------------------------------------------
	
	var popupOverlay *fyne.Container

	// Tombol YES / NO
	popupBtnNo := widget.NewButton("NO", func() {
		popupOverlay.Hide()
	})
	popupBtnNo.Importance = widget.DangerImportance // Merah

	popupBtnYes := widget.NewButton("YES", func() {
		popupOverlay.Hide()
		autoInstallKernel()
	})
	popupBtnYes.Importance = widget.HighImportance // Biru

	// [FIX] Mengatur ukuran tombol agar tidak terlalu panjang
	// Kita bungkus masing-masing tombol dengan ukuran tetap (Fixed Size)
	// Misal 140x40 cukup lebar tapi tidak memenuhi layar
	popupBtnSize := fyne.NewSize(140, 40)
	
	noWrapper := container.NewGridWrap(popupBtnSize, popupBtnNo)
	yesWrapper := container.NewGridWrap(popupBtnSize, popupBtnYes)

	// [FIX] Layout HBox dengan Jarak (Gap) Manual
	// Spacer kiri-kanan agar posisi di tengah, label di tengah sebagai pemisah (gap)
	popupBtns := container.NewHBox(
		layout.NewSpacer(), 
		noWrapper, 
		widget.NewLabel("        "), // Gap Manual yang cukup lebar
		yesWrapper, 
		layout.NewSpacer(),
	)

	// Isi Popup
	popupTitle := canvas.NewText("Inject Driver", theme.ForegroundColor())
	popupTitle.TextSize = 20
	popupTitle.TextStyle = fyne.TextStyle{Bold: true}
	popupTitle.Alignment = fyne.TextAlignCenter

	popupMsg := widget.NewLabel("Start automatic injection process?")
	popupMsg.Alignment = fyne.TextAlignCenter

	popupContent := container.NewVBox(
		widget.NewLabel(" "), // Spacer Atas
		container.NewCenter(popupTitle),
		widget.NewLabel(" "), // Spacer Tengah
		popupMsg,
		layout.NewSpacer(), // Dorong tombol ke bawah
		popupBtns, // Tombol di Bawah (Sudah diatur layoutnya diatas)
		widget.NewLabel(" "), // Sedikit spacer bawah
	)

	// Card/Box Popup
	popupCard := widget.NewCard("", "", container.NewPadded(popupContent))
	
	// Ukuran Popup (Besar tapi tidak full screen) - Ukuran yang Anda suka
	popupBox := container.NewGridWrap(fyne.NewSize(550, 240), popupCard)
	
	// Background Redup (Dimmed)
	dimmedBg := canvas.NewRectangle(color.RGBA{R: 0, G: 0, B: 0, A: 200})

	// Container Overlay
	popupOverlay = container.NewStack(dimmedBg, container.NewCenter(popupBox))
	popupOverlay.Hide() 

	// Tombol Header "Inject Driver"
	installBtn := widget.NewButtonWithIcon("Inject Driver", theme.DownloadIcon(), func() {
		popupOverlay.Show()
	})
	installBtn.Importance = widget.MediumImportance

	// -------------------------------------------------------------

	// Container Buttons Header
	selinuxContainer := container.NewGridWrap(btnSize, selinuxBtn)
	installContainer := container.NewGridWrap(btnSize, installBtn)
	clearContainer := container.NewGridWrap(btnSize, clearStack)

	headerRight := container.NewHBox(
		installContainer,
		widget.NewLabel(" "), 
		selinuxContainer,
		widget.NewLabel(" "), 
		clearContainer,
	)

	headerContent := container.NewBorder(nil, nil, container.NewPadded(headerLeft), headerRight)
	headerBgRect := canvas.NewRectangle(grayHeaderColor)
	
	headerBarWithBg := container.NewStack(
		headerBgRect,
		container.NewPadded(headerContent),
	)

	topSection := container.NewVBox(headerBarWithBg, container.NewPadded(status), widget.NewSeparator())
	
	footerStatusBox := container.NewHBox(layout.NewSpacer(), lblSystemTitle, lblSystemValue, layout.NewSpacer())
	
	sendBtn := widget.NewButtonWithIcon("Kirim", theme.MailSendIcon(), send)
	bigSendBtn := container.NewGridWrap(fyne.NewSize(120, 60), sendBtn)
	inputArea := container.NewBorder(nil, nil, nil, container.NewHBox(widget.NewLabel("   "), bigSendBtn), container.NewPadded(input))
	bottomSection := container.NewVBox(footerStatusBox, container.NewPadded(container.NewPadded(inputArea)))

	// Background Terminal
	bgImg := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "bg.png", StaticContent: bgPng})
	bgImg.FillMode = canvas.ImageFillStretch 

	terminalWithBg := container.NewStack(
		bgImg,       
		term.scroll, 
	)

	mainLayer := container.NewBorder(topSection, bottomSection, nil, nil, terminalWithBg)
	
	// FAB (FD)
	img := canvas.NewImageFromResource(&fyne.StaticResource{StaticName: "fd.png", StaticContent: fdPng})
	img.FillMode = canvas.ImageFillContain

	clickableIcon := container.NewStack(
		container.NewGridWrap(fyne.NewSize(60, 60), img),
		widget.NewButton("", func() {
			dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) { if r != nil { runFile(r) } }, w).Show()
		}),
	)
	clickableIcon.Objects[1].(*widget.Button).Importance = widget.LowImportance

	fabContainer := container.NewVBox(
		layout.NewSpacer(),
		container.NewHBox(layout.NewSpacer(), clickableIcon, widget.NewLabel(" ")),
		widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "), widget.NewLabel(" "),
	)
	
	// Tambahkan popupOverlay di paling atas stack
	w.SetContent(container.NewStack(mainLayer, fabContainer, popupOverlay))
	w.ShowAndRun()
}

