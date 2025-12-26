package main

import (
	"bytes"
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

func CheckKernelDriver() bool {
	if _, err := os.Stat(FlagFile); err == nil { return true }
	cmd := exec.Command("su", "-c", "ls -d /sys/module/"+TargetDriverName)
	if err := cmd.Run(); err == nil { return true }
	return false 
}

func CheckSELinux() string {
	cmd := exec.Command("su", "-c", "getenforce")
	out, err := cmd.Output()
	if err != nil { return "Unknown" }
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

	exec.Command("su", "-c", "rm -f "+FlagFile).Run()

	w := a.NewWindow("Simple Exec by TANGSAN")
	w.Resize(fyne.NewSize(720, 520))
	w.SetMaster()

	term := NewTerminal()
	brightYellow := color.RGBA{R: 255, G: 255, B: 0, A: 255}
	input := widget.NewEntry()
	input.SetPlaceHolder("Terminal Command...")
	status := widget.NewLabel("System: Ready")
	status.TextStyle = fyne.TextStyle{Bold: true}
	var stdin io.WriteCloser

	lblKernelTitle := canvas.NewText("KERNEL: ", brightYellow)
	lblKernelTitle.TextSize = 10; lblKernelTitle.TextStyle = fyne.TextStyle{Bold: true}
	lblKernelValue := canvas.NewText("CHECKING...", color.RGBA{150, 150, 150, 255})
	lblKernelValue.TextSize = 10; lblKernelValue.TextStyle = fyne.TextStyle{Bold: true}

	lblSELinuxTitle := canvas.NewText("SELINUX: ", brightYellow)
	lblSELinuxTitle.TextSize = 10; lblSELinuxTitle.TextStyle = fyne.TextStyle{Bold: true}
	lblSELinuxValue := canvas.NewText("CHECKING...", color.RGBA{150, 150, 150, 255})
	lblSELinuxValue.TextSize = 10; lblSELinuxValue.TextStyle = fyne.TextStyle{Bold: true}

	updateAllStatus := func() {
		go func() {
			if CheckKernelDriver() {
				lblKernelValue.Text = "DETECTED"; lblKernelValue.Color = color.RGBA{0, 255, 0, 255} 
			} else {
				lblKernelValue.Text = "NOT FOUND"; lblKernelValue.Color = color.RGBA{255, 50, 50, 255} 
			}
			lblKernelValue.Refresh()

			seStatus := CheckSELinux()
			lblSELinuxValue.Text = seStatus
			if seStatus == "Enforcing" {
				lblSELinuxValue.Color = color.RGBA{0, 255, 0, 255} // HIJAU
			} else {
				lblSELinuxValue.Color = color.RGBA{255, 50, 50, 255} // MERAH
			}
			lblSELinuxValue.Refresh()
		}()
	}
	updateAllStatus()

	autoInstallKernel := func() {
		term.Clear()
		status.SetText("System: Installing...")
		go func() {
			exec.Command("su", "-c", "rm -f "+FlagFile).Run()
			updateAllStatus() 
			term.Write([]byte("\x1b[36m╔══════════════════════════════════════╗\x1b[0m\n"))
			term.Write([]byte("\x1b[36m║      KERNEL DRIVER INSTALLER         ║\x1b[0m\n"))
			term.Write([]byte("\x1b[36m╚══════════════════════════════════════╝\x1b[0m\n"))
			out, err := exec.Command("uname", "-r").Output()
			if err != nil { term.Write([]byte("\x1b[31m[X] Kernel Error\x1b[0m\n")); return }
			rawVersion := strings.TrimSpace(string(out))
			term.Write([]byte(fmt.Sprintf(" -> Target: \x1b[33m%s\x1b[0m\n\n", rawVersion)))

			downloadPath := "/data/local/tmp/temp_kernel_dl"; targetFile := "/data/local/tmp/kernel_installer.sh"
			var downloadUrl string; var found bool = false

			simulateProgress := func(l string) {
				for i := 0; i <= 100; i += 10 { drawProgressBar(term, l, i, "\x1b[36m"); time.Sleep(50 * time.Millisecond) }
				term.Write([]byte("\n"))
			}

			targets := []string{rawVersion + ".sh"}
			parts := strings.Split(rawVersion, "-")
			if len(parts) > 0 { targets = append(targets, parts[0]+".sh") }

			for _, t := range targets {
				url := GitHubRepo + t
				term.Write([]byte(fmt.Sprintf("\x1b[97m[*] Checking: %s...\x1b[0m\n", t)))
				simulateProgress("Connecting")
				err, _ = downloadFile(url, downloadPath)
				if err == nil { downloadUrl = url; found = true; break }
			}

			if !found {
				term.Write([]byte("\n\x1b[31m[X] DRIVER NOT FOUND\x1b[0m\n")); status.SetText("System: Failed")
			} else {
				term.Write([]byte("\n\x1b[92m[*] Script Found: " + downloadUrl + "\x1b[0m\n"))
				exec.Command("su", "-c", "mv "+downloadPath+" "+targetFile).Run()
				exec.Command("su", "-c", "chmod 777 "+targetFile).Run()
				cmd := exec.Command("su", "-c", "sh "+targetFile)
				cmd.Env = append(os.Environ(), "TERM=xterm-256color")
				pStdin, _ := cmd.StdinPipe(); cmd.Stdout = term; cmd.Stderr = term; cmd.Run()
				
				if VerifySuccessAndCreateFlag() {
					term.Write([]byte("\n\x1b[32m[SUCCESS] Driver Injected.\x1b[0m\n"))
				} else {
					term.Write([]byte("\n\x1b[31m[FAILED] Driver not loaded into system.\x1b[0m\n"))
					exec.Command("su", "-c", "rm -f "+FlagFile).Run()
				}
				pStdin.Close(); time.Sleep(time.Second); updateAllStatus(); status.SetText("System: Online")
			}
		}()
	}

	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		term.Clear(); status.SetText("Status: Processing...")
		data, _ := io.ReadAll(reader); target := "/data/local/tmp/temp_exec"; isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))
		go func() {
			exec.Command("su", "-c", "rm -f "+target).Run()
			copyCmd := exec.Command("su", "-c", "cat > "+target+" && chmod 777 "+target)
			in, _ := copyCmd.StdinPipe(); go func() { defer in.Close(); in.Write(data) }(); copyCmd.Run()
			var cmd *exec.Cmd
			if isBinary { cmd = exec.Command("su", "-c", target) } else { cmd = exec.Command("su", "-c", "sh "+target) }
			cmd.Env = append(os.Environ(), "TERM=xterm-256color"); stdin, _ = cmd.StdinPipe(); cmd.Stdout = term; cmd.Stderr = term; cmd.Run()
			if VerifySuccessAndCreateFlag() { term.Write([]byte("\n\x1b[32m[SUCCESS] Kernel Active.\x1b[0m\n")) }
			status.SetText("Status: Idle"); stdin = nil; time.Sleep(500 * time.Millisecond); updateAllStatus()
		}()
	}

	send := func() {
		if stdin != nil && input.Text != "" {
			fmt.Fprintln(stdin, input.Text)
			term.Write([]byte(fmt.Sprintf("\x1b[36m> %s\x1b[0m\n", input.Text)))
			input.SetText("")
		}
	}
	input.OnSubmitted = func(string) { send() }

	headerLeft := container.NewVBox(canvas.NewText("Simple Exec by TANGSAN", theme.ForegroundColor()), container.NewHBox(lblKernelTitle, lblKernelValue), container.NewHBox(lblSELinuxTitle, lblSELinuxValue))
	
	// FITUR SELINUX SWITCH (GANTI SCAN)
	switchBtn := widget.NewButtonWithIcon("SELinux Switch", theme.ShieldIcon(), func() {
		go func() {
			current := CheckSELinux()
			target := "1" // Enforcing
			if current == "Enforcing" { target = "0" } // Permissive
			exec.Command("su", "-c", "setenforce "+target).Run()
			term.Write([]byte("\n\x1b[33m[*] Switching SELinux mode...\x1b[0m\n"))
			time.Sleep(200 * time.Millisecond); updateAllStatus()
		}()
	})

	installBtn := widget.NewButtonWithIcon("Inject Driver", theme.DownloadIcon(), func() {
		dialog.ShowConfirm("Inject", "Start process?", func(ok bool) { if ok { autoInstallKernel() } }, w)
	})
	clearBtn := widget.NewButtonWithIcon("", theme.ContentClearIcon(), func() { term.Clear() })
	headerRight := container.NewHBox(installBtn, switchBtn, clearBtn)
	
	headerBar := container.NewBorder(nil, nil, container.NewPadded(headerLeft), headerRight)
	topSection := container.NewVBox(headerBar, container.NewPadded(status), widget.NewSeparator())
	
	lblSystemTitle := canvas.NewText("SYSTEM: ", brightYellow)
	lblSystemTitle.TextSize = 10; lblSystemTitle.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
	lblSystemValue := canvas.NewText("ROOT ACCESS GRANTED", color.RGBA{0, 255, 0, 255})
	lblSystemValue.TextSize = 10; lblSystemValue.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
	footerStatusBox := container.NewHBox(layout.NewSpacer(), lblSystemTitle, lblSystemValue, layout.NewSpacer())
	
	sendBtn := widget.NewButtonWithIcon("Kirim", theme.MailSendIcon(), send)
	bigSendBtn := container.NewGridWrap(fyne.NewSize(120, 60), sendBtn)
	bigInput := container.NewPadded(input) 
	inputArea := container.NewBorder(nil, nil, nil, container.NewHBox(widget.NewLabel("   "), bigSendBtn), bigInput)
	inputContainer := container.NewPadded(container.NewPadded(inputArea))
	
	bottomSection := container.NewVBox(footerStatusBox, inputContainer)
	mainLayer := container.NewBorder(topSection, bottomSection, nil, nil, term.scroll)
	
	fabBtn := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) { if r != nil { runFile(r) } }, w).Show()
	})
	fabBtn.Importance = widget.HighImportance
	hugeFab := container.NewGridWrap(fyne.NewSize(100, 100), fabBtn)

	fabContainer := container.NewVBox(layout.NewSpacer(), container.NewHBox(layout.NewSpacer(), hugeFab, widget.NewLabel(" ")), widget.NewLabel(" "), widget.NewLabel(" "))
	w.SetContent(container.NewStack(mainLayer, fabContainer)); w.ShowAndRun()
}

