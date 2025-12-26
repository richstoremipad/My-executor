package main

import (
	"fmt"
	"image/color"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const GitHubRepo = "https://raw.githubusercontent.com/richstoremipad/My-executor/main/Driver/"
const FlagFile = "/dev/status_driver_aktif"
const TargetDriverName = "5.10_A12" 

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
	defStyle := &widget.CustomTextGridStyle{FGColor: theme.ForegroundColor(), BGColor: color.Transparent}
	re := regexp.MustCompile(`\x1b\[([0-9;]*)?([a-zA-Z])`)
	return &Terminal{grid: g, scroll: container.NewScroll(g), curStyle: defStyle, reAnsi: re}
}

func (t *Terminal) Clear() {
	t.grid.SetText("")
	t.curRow = 0
	t.curCol = 0
}

func (t *Terminal) Write(p []byte) (int, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	raw := strings.ReplaceAll(string(p), "\r\n", "\n")
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
	if codeSeq[len(codeSeq)-1] == 'm' {
		content := codeSeq[2 : len(codeSeq)-1]
		parts := strings.Split(content, ";")
		for _, part := range parts {
			if part == "31" { t.curStyle.FGColor = theme.ErrorColor() }
			if part == "32" { t.curStyle.FGColor = theme.SuccessColor() }
			if part == "36" { t.curStyle.FGColor = color.RGBA{0, 255, 255, 255} }
			if part == "0" || part == "" { t.curStyle.FGColor = theme.ForegroundColor() }
		}
	} else if codeSeq[len(codeSeq)-1] == 'J' {
		t.Clear()
	}
}

func (t *Terminal) printText(text string) {
	for _, char := range text {
		if char == '\n' { t.curRow++; t.curCol = 0; continue }
		if char == '\r' { t.curCol = 0; continue }
		for t.curRow >= len(t.grid.Rows) {
			t.grid.SetRow(len(t.grid.Rows), widget.TextGridRow{})
		}
		rowCells := t.grid.Rows[t.curRow].Cells
		if t.curCol >= len(rowCells) {
			newCells := make([]widget.TextGridCell, t.curCol+1)
			copy(newCells, rowCells)
			t.grid.SetRow(t.curRow, widget.TextGridRow{Cells: newCells})
		}
		style := *t.curStyle
		t.grid.SetCell(t.curRow, t.curCol, widget.TextGridCell{Rune: char, Style: &style})
		t.curCol++
	}
}

func CheckKernelDriver() bool {
	if _, err := os.Stat(FlagFile); err == nil { return true }
	return exec.Command("su", "-c", "ls -d /sys/module/"+TargetDriverName).Run() == nil
}

func CheckSELinux() string {
	out, err := exec.Command("su", "-c", "getenforce").Output()
	if err != nil { return "Unknown" }
	return strings.TrimSpace(string(out))
}

func VerifyAndFlag() bool {
	if exec.Command("su", "-c", "ls -d /sys/module/"+TargetDriverName).Run() == nil {
		exec.Command("su", "-c", "touch "+FlagFile+" && chmod 777 "+FlagFile).Run()
		return true
	}
	return false
}

func downloadFile(url string, filepath string) error {
	exec.Command("su", "-c", "rm -f "+filepath).Run()
	resp, err := http.Get(url)
	if err != nil { return err }
	defer resp.Body.Close()
	f, _ := os.Create("/data/local/tmp/t_dl")
	io.Copy(f, resp.Body)
	f.Close()
	exec.Command("su", "-c", "cp /data/local/tmp/t_dl "+filepath).Run()
	return nil
}

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

	lblKTitle := canvas.NewText("KERNEL: ", brightYellow)
	lblKVal := canvas.NewText("CHECKING...", color.Gray{Y: 150})
	lblKTitle.TextSize = 10; lblKVal.TextSize = 10

	lblSTitle := canvas.NewText("SELINUX: ", brightYellow)
	lblSVal := canvas.NewText("CHECKING...", color.Gray{Y: 150})
	lblSTitle.TextSize = 10; lblSVal.TextSize = 10

	update := func() {
		go func() {
			if CheckKernelDriver() {
				lblKVal.Text = "DETECTED"; lblKVal.Color = color.RGBA{0, 255, 0, 255}
			} else {
				lblKVal.Text = "NOT FOUND"; lblKVal.Color = color.RGBA{255, 50, 50, 255}
			}
			lblKVal.Refresh()
			se := CheckSELinux()
			lblSVal.Text = se
			if se == "Enforcing" { lblSVal.Color = color.RGBA{0, 255, 0, 255} } else { lblSVal.Color = color.RGBA{255, 50, 50, 255} }
			lblSVal.Refresh()
		}()
	}
	update()

	runFile := func(r fyne.URIReadCloser) {
		defer r.Close(); term.Clear()
		buf, _ := io.ReadAll(r); target := "/data/local/tmp/temp_exec"
		go func() {
			f, _ := os.Create("/data/local/tmp/t_wr")
			f.Write(buf); f.Close()
			exec.Command("su", "-c", "cp /data/local/tmp/t_wr "+target+" && chmod 777 "+target).Run()
			cmd := exec.Command("su", "-c", "sh "+target)
			stdin, _ = cmd.StdinPipe(); cmd.Stdout = term; cmd.Stderr = term; cmd.Run()
			VerifyAndFlag(); stdin = nil; update()
		}()
	}

	send := func() {
		if stdin != nil && input.Text != "" {
			fmt.Fprintln(stdin, input.Text)
			term.Write([]byte("> " + input.Text + "\n"))
			input.SetText("")
		}
	}

	switchBtn := widget.NewButtonWithIcon("SELinux Switch", theme.InfoIcon(), func() {
		go func() {
			t := "1"; if CheckSELinux() == "Enforcing" { t = "0" }
			exec.Command("su", "-c", "setenforce "+t).Run(); update()
		}()
	})

	header := container.NewBorder(nil, nil, 
		container.NewVBox(canvas.NewText("Simple Exec by TANGSAN", theme.ForegroundColor()), container.NewHBox(lblKTitle, lblKVal), container.NewHBox(lblSTitle, lblSVal)),
		container.NewHBox(widget.NewButtonWithIcon("Inject", theme.DownloadIcon(), func() {
			term.Clear(); update()
			out, _ := exec.Command("uname", "-r").Output()
			v := strings.TrimSpace(string(out))
			dl := "/data/local/tmp/k.sh"
			if err := downloadFile(GitHubRepo+v+".sh", dl); err == nil {
				exec.Command("su", "-c", "chmod 777 "+dl).Run()
				cmd := exec.Command("su", "-c", "sh "+dl)
				cmd.Stdout = term; cmd.Stderr = term; p, _ := cmd.StdinPipe(); cmd.Run(); p.Close()
				if VerifyAndFlag() { term.Write([]byte("SUCCESS\n")) } else { term.Write([]byte("FAILED\n")) }
			}
			update()
		}), switchBtn, widget.NewButtonWithIcon("", theme.ContentClearIcon(), term.Clear)),
	)

	bigSend := container.NewGridWrap(fyne.NewSize(150, 80), widget.NewButtonWithIcon("Kirim", theme.MailSendIcon(), send))
	inputArea := container.NewBorder(nil, nil, nil, bigSend, container.NewPadded(input))
	
	sysTitle := canvas.NewText("SYSTEM: ", brightYellow)
	sysVal := canvas.NewText("ROOT ACCESS GRANTED", color.RGBA{0, 255, 0, 255})
	sysTitle.TextSize = 10; sysVal.TextSize = 10
	
	bottom := container.NewVBox(container.NewHBox(layout.NewSpacer(), sysTitle, sysVal, layout.NewSpacer()), container.NewPadded(inputArea))
	
	fab := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) { if r != nil { runFile(r) } }, w).Show()
	})
	fab.Importance = widget.HighImportance
	hugeFab := container.NewGridWrap(fyne.NewSize(120, 120), fab)

	w.SetContent(container.NewStack(
		container.NewBorder(container.NewVBox(header, widget.NewSeparator(), status), bottom, nil, nil, term.scroll),
		container.NewVBox(layout.NewSpacer(), container.NewHBox(layout.NewSpacer(), hugeFab, widget.NewLabel(" ")), widget.NewLabel(" ")),
	))
	w.ShowAndRun()
}


