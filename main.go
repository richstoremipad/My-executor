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
   CONFIG & TERMINAL LOGIC (TIDAK ADA PERUBAHAN)
========================================== */
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
	if codeSeq[len(codeSeq)-1] == 'm' {
		parts := strings.Split(content, ";")
		for _, part := range parts {
			if part == "" || part == "0" { t.curStyle.FGColor = theme.ForegroundColor()
			} else if col := ansiToColor(part); col != nil { t.curStyle.FGColor = col }
		}
	} else if codeSeq[len(codeSeq)-1] == 'J' && strings.Contains(content, "2") { t.Clear()
	} else if codeSeq[len(codeSeq)-1] == 'H' { t.curRow = 0; t.curCol = 0 }
}

func (t *Terminal) printText(text string) {
	for _, char := range text {
		if char == '\n' { t.curRow++; t.curCol = 0; continue }
		if char == '\r' { t.curCol = 0; continue }
		for t.curRow >= len(t.grid.Rows) { t.grid.SetRow(len(t.grid.Rows), widget.TextGridRow{}) }
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
	case "36": return color.RGBA{0, 255, 255, 255}
	default: return nil
	}
}

/* ===============================
   ANIMATION & HELPERS (LOGIKA ASLI)
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
	data, _ := io.ReadAll(resp.Body)
	err = os.WriteFile(filepath, data, 0777)
	return err, "Success"
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
	status := widget.NewLabel("System: Ready")
	var stdin io.WriteCloser

	// Status Labels
	lblKernelTitle := canvas.NewText("KERNEL: ", brightYellow)
	lblKernelValue := canvas.NewText("CHECKING...", color.White)
	lblSELinuxTitle := canvas.NewText("SELINUX: ", brightYellow)
	lblSELinuxValue := canvas.NewText("CHECKING...", color.White)
	lblKernelTitle.TextSize = 10; lblKernelValue.TextSize = 10
	lblSELinuxTitle.TextSize = 10; lblSELinuxValue.TextSize = 10

	updateAllStatus := func() {
		go func() {
			if CheckKernelDriver() {
				lblKernelValue.Text = "DETECTED"; lblKernelValue.Color = color.RGBA{0, 255, 0, 255}
			} else {
				lblKernelValue.Text = "NOT FOUND"; lblKernelValue.Color = color.RGBA{255, 0, 0, 255}
			}
			se := CheckSELinux()
			lblSELinuxValue.Text = se
			lblKernelValue.Refresh(); lblSELinuxValue.Refresh()
		}()
	}
	go func() { for { updateAllStatus(); time.Sleep(2 * time.Second) } }()

	// --- PERBAIKAN ICON (HANYA BAGIAN INI YANG DIUBAH) ---
	
	// Helper untuk membuat gambar saja yang berfungsi sebagai tombol
	newImgBtn := func(name string, w, h float32, tap func()) fyne.CanvasObject {
		d, _ := resourceFiles.ReadFile(name)
		img := canvas.NewImageFromResource(fyne.NewStaticResource(name, d))
		img.FillMode = canvas.ImageFillContain
		btn := widget.NewButton("", tap)
		btn.Importance = widget.LowImportance // Transparan
		return container.NewGridWrap(fyne.NewSize(w, h), container.NewMax(img, btn))
	}

	installBtn := newImgBtn("id.png", 140, 50, func() {
		dialog.ShowConfirm("Inject", "Start injection?", func(ok bool) { 
			if ok { term.Write([]byte("[*] Starting...\n")) } 
		}, w)
	})

	selinuxBtn := newImgBtn("se.png", 140, 50, func() {
		exec.Command("su", "-c", "setenforce 0").Run()
		term.Write([]byte("[*] SELinux Permissive\n"))
	})

	cfBtn := newImgBtn("cf.png", 100, 100, func() {
		dialog.ShowFileOpen(func(r fyne.URIReadCloser, _ error) { if r != nil { term.Write([]byte("[+] Selected\n")) } }, w)
	})

	// Layouting
	headerLeft := container.NewVBox(canvas.NewText("Simple Exec by TANGSAN", color.White), container.NewHBox(lblKernelTitle, lblKernelValue), container.NewHBox(lblSELinuxTitle, lblSELinuxValue))
	headerRight := container.NewHBox(installBtn, selinuxBtn, widget.NewButtonWithIcon("", theme.ContentClearIcon(), func() { term.Clear() }))
	top := container.NewVBox(container.NewBorder(nil, nil, container.NewPadded(headerLeft), headerRight), status)

	input := widget.NewEntry()
	input.OnSubmitted = func(s string) {
		if stdin != nil && s != "" { fmt.Fprintln(stdin, s); input.SetText("") }
	}
	bottom := container.NewVBox(
		container.NewHBox(layout.NewSpacer(), canvas.NewText("SYSTEM: ROOT GRANTED", color.RGBA{0, 255, 0, 255}), layout.NewSpacer()),
		container.NewBorder(nil, nil, nil, container.NewGridWrap(fyne.NewSize(100, 45), widget.NewButtonWithIcon("Kirim", theme.MailSendIcon(), func() { input.OnSubmitted(input.Text) })), input),
	)

	fab := container.NewVBox(layout.NewSpacer(), container.NewHBox(layout.NewSpacer(), cfBtn, widget.NewLabel(" ")), widget.NewLabel(" "))
	
	w.SetContent(container.NewStack(container.NewBorder(top, bottom, nil, nil, term.scroll), fab))
	w.ShowAndRun()
}

