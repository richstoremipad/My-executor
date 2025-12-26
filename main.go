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
   SYSTEM HELPERS
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

	w := a.NewWindow("Simple Exec by TANGSAN")
	w.Resize(fyne.NewSize(720, 520))
	w.SetMaster()

	term := NewTerminal()
	brightYellow := color.RGBA{R: 255, G: 255, B: 0, A: 255}
	input := widget.NewEntry()
	input.SetPlaceHolder("Terminal Command...")
	status := widget.NewLabel("System: Ready")
	var stdin io.WriteCloser

	lblKernelTitle := canvas.NewText("KERNEL: ", brightYellow)
	lblKernelTitle.TextSize = 10
	lblKernelValue := canvas.NewText("CHECKING...", color.RGBA{150, 150, 150, 255})
	lblKernelValue.TextSize = 10

	lblSELinuxTitle := canvas.NewText("SELINUX: ", brightYellow)
	lblSELinuxTitle.TextSize = 10
	lblSELinuxValue := canvas.NewText("CHECKING...", color.RGBA{150, 150, 150, 255})
	lblSELinuxValue.TextSize = 10

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
				lblSELinuxValue.Color = color.RGBA{0, 255, 0, 255}
			} else {
				lblSELinuxValue.Color = color.RGBA{255, 50, 50, 255}
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
			out, _ := exec.Command("uname", "-r").Output()
			rawVersion := strings.TrimSpace(string(out))
			downloadPath := "/data/local/tmp/temp_dl"
			targetFile := "/data/local/tmp/installer.sh"
			
			err, _ := downloadFile(GitHubRepo+rawVersion+".sh", downloadPath)
			if err != nil {
				term.Write([]byte("\x1b[31m[X] Driver Not Found on Server.\x1b[0m\n"))
			} else {
				exec.Command("su", "-c", "mv "+downloadPath+" "+targetFile+" && chmod 777 "+targetFile).Run()
				cmd := exec.Command("su", "-c", "sh "+targetFile)
				cmd.Stdout = term; cmd.Stderr = term; pStdin, _ := cmd.StdinPipe(); cmd.Run(); pStdin.Close()
				
				if VerifySuccessAndCreateFlag() {
					term.Write([]byte("\n\x1b[32m[SUCCESS] Driver Active.\x1b[0m\n"))
				} else {
					term.Write([]byte("\n\x1b[31m[FAILED] Log says success but driver not found in system.\x1b[0m\n"))
				}
			}
			updateAllStatus(); status.SetText("System: Online")
		}()
	}

	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		term.Clear(); status.SetText("Status: Processing...")
		data, _ := io.ReadAll(reader); target := "/data/local/tmp/temp_exec"
		go func() {
			exec.Command("su", "-c", "cat > "+target+" && chmod 777 "+target).Run() // Langsung buat file
			cmd := exec.Command("su", "-c", "sh "+target)
			stdin, _ = cmd.StdinPipe(); cmd.Stdout = term; cmd.Stderr = term; cmd.Run()
			VerifySuccessAndCreateFlag()
			status.SetText("Status: Idle"); stdin = nil; updateAllStatus()
		}()
	}

	send := func() {
		if stdin != nil && input.Text != "" {
			fmt.Fprintln(stdin, input.Text)
			term.Write([]byte("> " + input.Text + "\n"))
			input.SetText("")
		}
	}
	input.OnSubmitted = func(string) { send() }

	// --- SELINUX SWITCH ---
	switchBtn := widget.NewButtonWithIcon("SELinux Switch", theme.InfoIcon(), func() {
		go func() {
			target := "1"; if CheckSELinux() == "Enforcing" { target = "0" }
			exec.Command("su", "-c", "setenforce "+target).Run()
			updateAllStatus()
		}()
	})

	installBtn := widget.NewButtonWithIcon("Inject Driver", theme.DownloadIcon(), autoInstallKernel)
	clearBtn := widget.NewButtonWithIcon("", theme.ContentClearIcon(), func() { term.Clear() })
	
	headerL := container.NewVBox(canvas.NewText("Simple Exec by TANGSAN", theme.ForegroundColor()), container.NewHBox(lblKernelTitle, lblKernelValue), container.NewHBox(lblSELinuxTitle, lblSELinuxValue))
	headerR := container.NewHBox(installBtn, switchBtn, clearBtn)
	headerBar := container.NewBorder(nil, nil, container.NewPadded(headerL), headerR)
	
	sendBtn := widget.NewButtonWithIcon("Kirim", theme.MailSendIcon(), send)
	bigSendBtn := container.NewGridWrap(fyne.NewSize(150, 80), sendBtn)
	inputContainer := container.NewPadded(container.NewBorder(nil, nil, nil, bigSendBtn, container.NewPadded(input)))
	
	lblSysTitle := canvas.NewText("SYSTEM: ", brightYellow)
	lblSysValue := canvas.NewText("ROOT ACCESS GRANTED", color.RGBA{0, 255, 0, 255})
	lblSysTitle.TextSize = 10; lblSysValue.TextSize = 10
	bottomSection := container.NewVBox(container.NewHBox(layout.NewSpacer(), lblSysTitle, lblSysValue, layout.NewSpacer()), inputContainer)

	fabBtn := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) { if r != nil { runFile(r) } }, w).Show()
	})
	fabBtn.Importance = widget.HighImportance
	hugeFab := container.NewGridWrap(fyne.NewSize(120, 120), fabBtn)

	fabPos := container.NewVBox(layout.NewSpacer(), container.NewHBox(layout.NewSpacer(), hugeFab, widget.NewLabel(" ")), widget.NewLabel(" "))

	w.SetContent(container.NewStack(container.NewBorder(container.NewVBox(headerBar, widget.NewSeparator(), status), bottomSection, nil, nil, term.scroll), fabPos))
	w.ShowAndRun()
}

