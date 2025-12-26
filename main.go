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
	case "35": return color.RGBA{200, 0, 200, 255}
	case "36": return color.RGBA{0, 255, 255, 255}
	case "37": return theme.ForegroundColor()
	case "90": return color.Gray{Y: 100}
	case "91": return color.RGBA{255, 100, 100, 255}
	case "92": return color.RGBA{100, 255, 100, 255}
	case "93": return color.RGBA{255, 255, 100, 255}
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
		t.handleAnsiCode(raw[loc[0]:loc[1]])
		raw = raw[loc[1]:]
	}
	t.grid.Refresh()
	t.scroll.ScrollToBottom()
	return len(p), nil
}

func (t *Terminal) handleAnsiCode(codeSeq string) {
	if len(codeSeq) < 3 { return }
	content := codeSeq[2 : len(codeSeq)-1]
	if codeSeq[len(codeSeq)-1] == 'm' {
		parts := strings.Split(content, ";")
		for _, part := range parts {
			if part == "" || part == "0" {
				t.curStyle.FGColor = theme.ForegroundColor()
			} else {
				col := ansiToColor(part)
				if col != nil { t.curStyle.FGColor = col }
			}
		}
	} else if codeSeq[len(codeSeq)-1] == 'J' {
		t.Clear()
	}
}

func (t *Terminal) printText(text string) {
	for _, char := range text {
		if char == '\n' {
			t.curRow++; t.curCol = 0; continue
		}
		if char == '\r' {
			t.curCol = 0; continue
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
		t.grid.SetCell(t.curRow, t.curCol, widget.TextGridCell{Rune: char, Style: &cellStyle})
		t.curCol++
	}
}

/* ===============================
   SYSTEM HELPERS
================================ */

func CheckKernelDriver() bool {
	if _, err := os.Stat(FlagFile); err == nil { return true }
	if err := exec.Command("su", "-c", "ls -d /sys/module/"+TargetDriverName).Run(); err == nil { return true }
	return false 
}

func CheckSELinux() string {
	out, err := exec.Command("su", "-c", "getenforce").Output()
	if err != nil { return "Unknown" }
	return strings.TrimSpace(string(out))
}

func VerifyAndFlag() bool {
	if err := exec.Command("su", "-c", "ls -d /sys/module/"+TargetDriverName).Run(); err == nil {
		exec.Command("su", "-c", "touch "+FlagFile+" && chmod 777 "+FlagFile).Run()
		return true
	}
	return false
}

func downloadFile(url string, filepath string) error {
	exec.Command("su", "-c", "rm -f "+filepath).Run()
	cmd := exec.Command("su", "-c", fmt.Sprintf("curl -k -L -o %s %s", filepath, url))
	if err := cmd.Run(); err == nil { return nil }

	resp, err := http.Get(url)
	if err != nil { return err }
	defer resp.Body.Close()
	f, _ := os.Create("/data/local/tmp/temp_dl")
	io.Copy(f, resp.Body)
	f.Close()
	exec.Command("su", "-c", "cp /data/local/tmp/temp_dl "+filepath).Run()
	return nil
}

/* ===============================
              MAIN UI
================================ */

func main() {
	a := app.New()
	a.Settings().SetTheme(theme.DarkTheme())

	w := a.NewWindow("Simple Exec by TANGSAN")
	w.Resize(fyne.NewSize(720, 520))

	term := NewTerminal()
	brightYellow := color.RGBA{255, 255, 0, 255}
	input := widget.NewEntry()
	status := widget.NewLabel("System: Ready")
	var stdin io.WriteCloser

	lblKernelTitle := canvas.NewText("KERNEL: ", brightYellow)
	lblKernelValue := canvas.NewText("CHECKING...", color.Gray{Y: 150})
	lblKernelTitle.TextSize = 10; lblKernelValue.TextSize = 10

	lblSELinuxTitle := canvas.NewText("SELINUX: ", brightYellow)
	lblSELinuxValue := canvas.NewText("CHECKING...", color.Gray{Y: 150})
	lblSELinuxTitle.TextSize = 10; lblSELinuxValue.TextSize = 10

	updateAll := func() {
		go func() {
			if CheckKernelDriver() {
				lblKernelValue.Text = "DETECTED"; lblKernelValue.Color = color.RGBA{0, 255, 0, 255}
			} else {
				lblKernelValue.Text = "NOT FOUND"; lblKernelValue.Color = color.RGBA{255, 50, 50, 255}
			}
			lblKernelValue.Refresh()

			se := CheckSELinux()
			lblSELinuxValue.Text = se
			if se == "Enforcing" { lblSELinuxValue.Color = color.RGBA{0, 255, 0, 255} } else { lblSELinuxValue.Color = color.RGBA{255, 50, 50, 255} }
			lblSELinuxValue.Refresh()
		}()
	}
	updateAll()

	autoInstall := func() {
		term.Clear(); status.SetText("System: Installing...")
		go func() {
			exec.Command("su", "-c", "rm -f "+FlagFile).Run()
			updateAll()
			out, _ := exec.Command("uname", "-r").Output()
			ver := strings.TrimSpace(string(out))
			dlPath := "/data/local/tmp/k_inst.sh"
			
			term.Write([]byte("[*] Searching Driver for: " + ver + "\n"))
			if err := downloadFile(GitHubRepo+ver+".sh", dlPath); err == nil {
				exec.Command("su", "-c", "chmod 777 "+dlPath).Run()
				cmd := exec.Command("su", "-c", "sh "+dlPath)
				cmd.Stdout = term; cmd.Stderr = term; p, _ := cmd.StdinPipe()
				cmd.Run(); p.Close()

				if VerifyAndFlag() {
					term.Write([]byte("\x1b[32m[SUCCESS] Driver Injected.\x1b[0m\n"))
				} else {
					term.Write([]byte("\x1b[31m[FAILED] Module not found in system.\x1b[0m\n"))
				}
			} else {
				term.Write([]byte("[X] Download Failed.\n"))
			}
			status.SetText("System: Online"); updateAll()
		}()
	}

	runFile := func(r fyne.URIReadCloser) {
		defer r.Close(); term.Clear()
		data, _ := io.ReadAll(r); target := "/data/local/tmp/temp_exec"
		go func() {
			exec.Command("su", "-c", "cat > "+target+" && chmod 777 "+target).Run()
			cmd := exec.Command("su", "-c", "sh "+target)
			stdin, _ = cmd.StdinPipe(); cmd.Stdout = term; cmd.Stderr = term; cmd.Run()
			VerifyAndFlag(); stdin = nil; updateAll()
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

	// UI JUMBO 5X
	switchBtn := widget.NewButtonWithIcon("SELinux Switch", theme.SecurityIcon(), func() { // theme.SecurityIcon sebagai pengganti Shield
		go func() {
			target := "1"; if CheckSELinux() == "Enforcing" { target = "0" }
			exec.Command("su", "-c", "setenforce "+target).Run()
			updateAll()
		}()
	})
	
	installBtn := widget.NewButtonWithIcon("Inject Driver", theme.DownloadIcon(), autoInstall)
	header := container.NewBorder(nil, nil, container.NewVBox(canvas.NewText("Simple Exec by TANGSAN", theme.ForegroundColor()), container.NewHBox(lblKernelTitle, lblKernelValue), container.NewHBox(lblSELinuxTitle, lblSELinuxValue)), container.NewHBox(installBtn, switchBtn, widget.NewButtonWithIcon("", theme.ContentClearIcon(), term.Clear)))
	
	sendBtn := widget.NewButtonWithIcon("Kirim", theme.MailSendIcon(), send)
	bigSend := container.NewGridWrap(fyne.NewSize(150, 80), sendBtn)
	inputArea := container.NewBorder(nil, nil, nil, bigSend, container.NewPadded(input))
	
	lblSysTitle := canvas.NewText("SYSTEM: ", brightYellow)
	lblSysVal := canvas.NewText("ROOT ACCESS GRANTED", color.RGBA{0, 255, 0, 255})
	lblSysTitle.TextSize = 10; lblSysVal.TextSize = 10
	
	bottom := container.NewVBox(container.NewHCenter(container.NewHBox(lblSysTitle, lblSysVal)), container.NewPadded(inputArea))
	
	fab := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) { if r != nil { runFile(r) } }, w).Show()
	})
	fab.Importance = widget.HighImportance
	hugeFab := container.NewGridWrap(fyne.NewSize(120, 120), fab)

	w.SetContent(container.NewStack(container.NewBorder(container.NewVBox(header, widget.NewSeparator()), bottom, nil, nil, term.scroll), container.NewVBox(layout.NewSpacer(), container.NewHBox(layout.NewSpacer(), hugeFab, widget.NewLabel(" ")), widget.NewLabel(" "))))
	w.ShowAndRun()
}

