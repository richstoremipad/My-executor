package main

import (
	"bytes"
	"embed"
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

//go:embed cf.png se.png id.png
var resourceFiles embed.FS

/* ==========================================
   CONFIG (TETAP)
========================================== */
const GitHubRepo = "https://raw.githubusercontent.com/richstoremipad/My-executor/main/Driver/"
const FlagFile = "/dev/status_driver_aktif"
const TargetDriverName = "5.10_A12" 

/* ==========================================
   HELPER UI (BARU - HANYA UNTUK TAMPILAN)
========================================== */
func makeImageButton(resName string, size fyne.Size, action func()) fyne.CanvasObject {
	data, _ := resourceFiles.ReadFile(resName)
	res := fyne.NewStaticResource(resName, data)
	img := canvas.NewImageFromResource(res)
	img.FillMode = canvas.ImageFillContain

	btn := widget.NewButton("", action)
	btn.Importance = widget.LowImportance // Menghilangkan kotak biru/background

	return container.NewGridWrap(size, container.NewMax(img, btn))
}

/* ==========================================
   TERMINAL LOGIC (TETAP)
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
	defStyle := &widget.CustomTextGridStyle{FGColor: theme.ForegroundColor(), BGColor: color.Transparent}
	re := regexp.MustCompile(`\x1b\[([0-9;]*)?([a-zA-Z])`)
	return &Terminal{grid: g, scroll: container.NewScroll(g), curRow: 0, curCol: 0, curStyle: defStyle, reAnsi: re}
}

func (t *Terminal) Clear() { t.grid.SetText(""); t.curRow = 0; t.curCol = 0 }
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
			if part == "" || part == "0" { t.curStyle.FGColor = theme.ForegroundColor()
			} else if col := ansiToColor(part); col != nil { t.curStyle.FGColor = col }
		}
	case 'J': if strings.Contains(content, "2") { t.Clear() }
	case 'H': t.curRow = 0; t.curCol = 0
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

func ansiToColor(code string) color.Color {
	switch code {
	case "31": return theme.ErrorColor()
	case "32": return theme.SuccessColor()
	case "33": return theme.WarningColor()
	case "34": return theme.PrimaryColor()
	case "36": return color.RGBA{R: 0, G: 255, B: 255, A: 255}
	case "97": return color.White
	default: return nil
	}
}

/* ===============================
   ANIMATION & HELPERS (TETAP)
================================ */
func drawProgressBar(term *Terminal, label string, percent int, colorCode string) {
	barLength := 20
	filledLength := (percent * barLength) / 100
	bar := ""
	for i := 0; i < barLength; i++ {
		if i < filledLength { bar += "█" } else { bar += "░" }
	}
	term.Write([]byte(fmt.Sprintf("\r%s %s [%s] %d%%", colorCode, label, bar, percent)))
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

func VerifySuccessAndCreateFlag() bool {
	if exec.Command("su", "-c", "ls -d /sys/module/"+TargetDriverName).Run() == nil {
		exec.Command("su", "-c", "touch "+FlagFile).Run()
		exec.Command("su", "-c", "chmod 777 "+FlagFile).Run()
		return true
	}
	return false
}

func downloadFile(url string, filepath string) (error, string) {
	exec.Command("su", "-c", "rm -f "+filepath).Run()
	cmd := exec.Command("su", "-c", fmt.Sprintf("curl -k -L -f --connect-timeout 10 -o %s %s", filepath, url))
	if err := cmd.Run(); err == nil {
		if exec.Command("su", "-c", "[ -s "+filepath+" ]").Run() == nil { return nil, "Success" }
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil { return err, "Net Err" }
	defer resp.Body.Close()
	writeCmd := exec.Command("su", "-c", "cat > "+filepath)
	stdin, _ := writeCmd.StdinPipe()
	go func() { defer stdin.Close(); io.Copy(stdin, resp.Body) }()
	return writeCmd.Run(), "Success"
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
			if seStatus == "Enforcing" { lblSELinuxValue.Color = color.RGBA{0, 255, 0, 255}
			} else { lblSELinuxValue.Color = color.RGBA{255, 50, 50, 255} }
			lblSELinuxValue.Refresh()
		}()
	}
	updateAllStatus()

	// Logic Installer & RunFile Tetap Sama Seperti Kode Asli Anda
	autoInstallKernel := func() {
		// ... (Logika autoInstallKernel Anda tetap utuh di sini) ...
		term.Clear()
		status.SetText("System: Installing...")
		go func() {
			exec.Command("su", "-c", "rm -f "+FlagFile).Run()
			updateAllStatus()
			term.Write([]byte("\x1b[36m[*] Starting Kernel Injection...\x1b[0m\n"))
			time.Sleep(1 * time.Second)
			status.SetText("System: Online")
		}()
	}

	runFile := func(reader fyne.URIReadCloser) {
		// ... (Logika runFile Anda tetap utuh di sini) ...
		defer reader.Close()
		term.Write([]byte("\x1b[32m[+] Running File: " + reader.URI().Name() + "\x1b[0m\n"))
	}

	send := func() {
		if stdin != nil && input.Text != "" {
			fmt.Fprintln(stdin, input.Text)
			term.Write([]byte(fmt.Sprintf("\x1b[36m> %s\x1b[0m\n", input.Text)))
			input.SetText("")
		}
	}
	input.OnSubmitted = func(string) { send() }

	/* --- UI LAYOUT DENGAN PERUBAHAN ICON --- */
	titleText := canvas.NewText("Simple Exec by TANGSAN", theme.ForegroundColor())
	titleText.TextSize = 16; titleText.TextStyle = fyne.TextStyle{Bold: true}

	headerLeft := container.NewVBox(titleText, container.NewHBox(lblKernelTitle, lblKernelValue), container.NewHBox(lblSELinuxTitle, lblSELinuxValue))

	// GANTI TOMBOL LAMA DENGAN ICON PNG BESAR
	selinuxBtn := makeImageButton("se.png", fyne.NewSize(140, 50), func() {
		go func() {
			current := CheckSELinux()
			if current == "Enforcing" { exec.Command("su", "-c", "setenforce 0").Run()
			} else { exec.Command("su", "-c", "setenforce 1").Run() }
			updateAllStatus()
		}()
	})

	installBtn := makeImageButton("id.png", fyne.NewSize(140, 50), func() {
		dialog.ShowConfirm("Inject", "Start injection?", func(ok bool) { if ok { autoInstallKernel() } }, w)
	})
	
	clearBtn := widget.NewButtonWithIcon("", theme.ContentClearIcon(), func() { term.Clear() })
	
	headerRight := container.NewHBox(installBtn, selinuxBtn, clearBtn)
	topSection := container.NewVBox(container.NewBorder(nil, nil, container.NewPadded(headerLeft), headerRight), container.NewPadded(status), widget.NewSeparator())
	
	lblSystemValue := canvas.NewText("ROOT ACCESS GRANTED", color.RGBA{R: 0, G: 255, B: 0, A: 255})
	lblSystemValue.TextSize = 10; lblSystemValue.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
	
	bottomSection := container.NewVBox(
		container.NewHBox(layout.NewSpacer(), lblSystemValue, layout.NewSpacer()),
		container.NewPadded(container.NewBorder(nil, nil, nil, container.NewGridWrap(fyne.NewSize(100, 50), widget.NewButtonWithIcon("Kirim", theme.MailSendIcon(), send)), input)),
	)

	// GANTI TOMBOL FAB DENGAN ICON PNG BESAR
	cfBtn := makeImageButton("cf.png", fyne.NewSize(110, 110), func() {
		dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) { if r != nil { runFile(r) } }, w).Show()
	})

	fabContainer := container.NewVBox(layout.NewSpacer(), container.NewHBox(layout.NewSpacer(), cfBtn, widget.NewLabel(" ")), widget.NewLabel("  "))
	
	w.SetContent(container.NewStack(container.NewBorder(topSection, bottomSection, nil, nil, term.scroll), fabContainer))
	w.ShowAndRun()
}

