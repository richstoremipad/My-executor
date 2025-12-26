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
   CONFIG
========================================== */
const GitHubRepo = "https://raw.githubusercontent.com/richstoremipad/My-executor/main/Driver/"
const FlagFile = "/dev/status_driver_aktif"
const TargetDriverName = "5.10_A12" 

/* ==========================================
   HELPER UNTUK ICON CUSTOM (TANPA TEKS & BOX BIRU)
========================================== */
func createImgButton(resName string, size fyne.Size, action func()) fyne.CanvasObject {
	data, _ := resourceFiles.ReadFile(resName)
	res := fyne.NewStaticResource(resName, data)
	img := canvas.NewImageFromResource(res)
	img.FillMode = canvas.ImageFillContain

	btn := widget.NewButton("", action)
	btn.Importance = widget.LowImportance // Menghapus background biru

	return container.NewGridWrap(size, container.NewMax(img, btn))
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
	switch codeSeq[len(codeSeq)-1] {
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
		for t.curRow >= len(t.grid.Rows) { t.grid.SetRow(len(t.grid.Rows), widget.TextGridRow{}) }
		rowCells := t.grid.Rows[t.curRow].Cells
		if t.curCol >= len(rowCells) {
			newCells := make([]widget.TextGridCell, t.curCol+1)
			copy(newCells, rowCells)
			t.grid.SetRow(t.curRow, widget.TextGridRow{Cells: newCells})
		}
		t.grid.SetCell(t.curRow, t.curCol, widget.TextGridCell{Rune: char, Style: t.curStyle})
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

/* ==========================================
   LOGIC HELPERS (DOWNLOAD, CHECK, DLL)
========================================== */
func CheckKernelDriver() bool {
	if _, err := os.Stat(FlagFile); err == nil { return true }
	return exec.Command("su", "-c", "ls -d /sys/module/"+TargetDriverName).Run() == nil
}

func CheckSELinux() string {
	out, err := exec.Command("su", "-c", "getenforce").Output()
	if err != nil { return "Unknown" }
	return strings.TrimSpace(string(out))
}

func downloadFile(url, path string) error {
	resp, err := http.Get(url)
	if err != nil { return err }
	defer resp.Body.Close()
	f, err := os.Create(path)
	if err != nil { return err }
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
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
	statusLabel := widget.NewLabel("System: Ready") // Digunakan agar tidak error "declared and not used"
	var stdin io.WriteCloser

	// Header Status
	lblKernel := canvas.NewText("KERNEL: CHECKING...", color.White)
	lblSELinux := canvas.NewText("SELINUX: CHECKING...", color.White)
	lblKernel.TextSize = 10; lblSELinux.TextSize = 10

	updateUI := func() {
		if CheckKernelDriver() {
			lblKernel.Text = "KERNEL: DETECTED"; lblKernel.Color = color.RGBA{0, 255, 0, 255}
		} else {
			lblKernel.Text = "KERNEL: NOT FOUND"; lblKernel.Color = color.RGBA{255, 0, 0, 255}
		}
		se := CheckSELinux()
		lblSELinux.Text = "SELINUX: " + se
		lblSELinux.Color = color.RGBA{255, 255, 0, 255}
		lblKernel.Refresh(); lblSELinux.Refresh()
	}
	go func() { for { updateUI(); time.Sleep(2 * time.Second) } }()

	// --- TOMBOL DENGAN PNG BESAR (PERMINTAAN USER) ---
	btnInject := createImgButton("id.png", fyne.NewSize(140, 55), func() {
		term.Write([]byte("\x1b[36m[*] Injecting Driver...\x1b[0m\n"))
	})

	btnSELinux := createImgButton("se.png", fyne.NewSize(140, 55), func() {
		exec.Command("su", "-c", "setenforce 0").Run()
		term.Write([]byte("\x1b[33m[*] SELinux Switched\x1b[0m\n"))
	})

	btnChoiceFile := createImgButton("cf.png", fyne.NewSize(110, 110), func() {
		dialog.ShowFileOpen(func(r fyne.URIReadCloser, _ error) {
			if r != nil { term.Write([]byte("\x1b[32m[+] Selected: " + r.URI().Name() + "\x1b[0m\n")) }
		}, w)
	})

	// Layouting
	headerLeft := container.NewVBox(canvas.NewText("Simple Exec by TANGSAN", color.RGBA{255, 255, 0, 255}), lblKernel, lblSELinux)
	headerRight := container.NewHBox(btnInject, btnSELinux)
	top := container.NewVBox(container.NewBorder(nil, nil, container.NewPadded(headerLeft), headerRight), statusLabel)

	input := widget.NewEntry()
	sendBtn := widget.NewButtonWithIcon("Kirim", theme.MailSendIcon(), func() {
		if stdin != nil && input.Text != "" { fmt.Fprintln(stdin, input.Text); input.SetText("") }
	})

	bottom := container.NewVBox(
		container.NewHBox(layout.NewSpacer(), canvas.NewText("SYSTEM: ROOT GRANTED", color.RGBA{0, 255, 0, 255}), layout.NewSpacer()),
		container.NewBorder(nil, nil, nil, container.NewGridWrap(fyne.NewSize(100, 45), sendBtn), container.NewPadded(input)),
	)

	fab := container.NewVBox(layout.NewSpacer(), container.NewHBox(layout.NewSpacer(), btnChoiceFile, widget.NewLabel(" ")), widget.NewLabel(" "))

	w.SetContent(container.NewStack(container.NewBorder(top, bottom, nil, nil, term.scroll), fab))
	w.ShowAndRun()
}

