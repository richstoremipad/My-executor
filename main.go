package main

import (
	"bytes"
	"fmt"
	"image/color"
	"io"
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
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

/* ==========================================
   1. INTERACTION LAYER (SOLUSI FINAL BACK BUTTON)
   Widget ini membungkus seluruh UI untuk menangkap Back Gesture
========================================== */

type InteractionLayer struct {
	widget.BaseWidget
	content fyne.CanvasObject
	onBack  func()
}

func NewInteractionLayer(content fyne.CanvasObject, onBack func()) *InteractionLayer {
	i := &InteractionLayer{content: content, onBack: onBack}
	i.ExtendBaseWidget(i)
	return i
}

func (i *InteractionLayer) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(i.content)
}

// Agar widget ini bisa menerima fokus dan event keyboard
func (i *InteractionLayer) FocusGained() {}
func (i *InteractionLayer) FocusLost()   {}
func (i *InteractionLayer) TypedRune(_ rune) {}

// Tangkap tombol Back (Escape) di sini
func (i *InteractionLayer) TypedKey(e *fyne.KeyEvent) {
	if e.Name == fyne.KeyEscape {
		if i.onBack != nil {
			i.onBack()
		}
	}
}

// Saat user tap area kosong (terminal/background), ambil fokus agar Back berfungsi
func (i *InteractionLayer) Tapped(_ *fyne.PointEvent) {
	// Ambil fokus ke diri sendiri
	if c := fyne.CurrentApp().Driver().CanvasForObject(i); c != nil {
		c.Focus(i)
	}
}

/* ==========================================
   2. CUSTOM INPUT (BACK SAAT MENGETIK)
========================================== */

type BackButtonEntry struct {
	widget.Entry
	onBack func()
}

func NewBackButtonEntry(onBack func()) *BackButtonEntry {
	entry := &BackButtonEntry{onBack: onBack}
	entry.ExtendBaseWidget(entry)
	return entry
}

func (e *BackButtonEntry) TypedKey(key *fyne.KeyEvent) {
	if key.Name == fyne.KeyEscape {
		if e.onBack != nil {
			e.onBack()
		}
		return // Jangan teruskan ke default handler (biar keyboard nutup tapi app gak exit)
	}
	e.Entry.TypedKey(key)
}

/* ==========================================
   3. TERMINAL WIDGET
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

	// TextGrid dimasukkan ke ScrollContainer
	scroll := container.NewScroll(g)

	return &Terminal{
		grid:     g,
		scroll:   scroll,
		curRow:   0,
		curCol:   0,
		curStyle: defStyle,
		reAnsi:   re,
	}
}

func ansiToColor(code string) color.Color {
	switch code {
	case "30", "90": return color.Gray{Y: 100}
	case "31", "91": return theme.ErrorColor()
	case "32", "92": return theme.SuccessColor()
	case "33", "93": return theme.WarningColor()
	case "34", "94": return theme.PrimaryColor()
	case "35", "95": return color.RGBA{R: 200, G: 0, B: 200, A: 255}
	case "36", "96": return theme.PrimaryColor()
	case "37", "97": return theme.ForegroundColor()
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
              MAIN
================================ */

func main() {
	a := app.New()
	a.Settings().SetTheme(theme.DarkTheme())

	w := a.NewWindow("Universal Root Executor")
	w.Resize(fyne.NewSize(720, 520))
	
	// Hapus SetMaster() karena bisa memicu auto-minimize di beberapa OS
	// w.SetMaster() 

	/* --- LOGIKA KELUAR --- */
	confirmExit := func() {
		dialog.ShowConfirm("Konfirmasi Keluar", "Yakin ingin menutup aplikasi?", func(ok bool) {
			if ok {
				a.Quit()
			}
		}, w)
	}

	// 1. Intercept Close Window (Desktop)
	w.SetCloseIntercept(confirmExit)

	// 2. Intercept Global Canvas Key (Fallback layer 1)
	w.Canvas().SetOnTypedKey(func(k *fyne.KeyEvent) {
		if k.Name == fyne.KeyEscape {
			confirmExit()
		}
	})

	/* TERMINAL SETUP */
	term := NewTerminal()

	// Hack: Tambahkan background hitam transparan di scroll agar tappable area luas
	// Namun TextGrid sudah cukup mengisi area.
	
	/* INPUT */
	// Gunakan Custom Entry agar saat ngetik pun Back kedetect
	input := NewBackButtonEntry(confirmExit)
	input.SetPlaceHolder("Ketik perintah...")

	status := widget.NewLabel("Status: Siap")
	status.TextStyle = fyne.TextStyle{Bold: true}

	var stdin io.WriteCloser

	/* RUN FILE FUNCTION */
	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		term.Clear()
		status.SetText("Status: Menyiapkan...")

		data, _ := io.ReadAll(reader)
		target := "/data/local/tmp/temp_exec"
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			copyCmd := exec.Command("su", "-c", "cat > "+target+" && chmod 777 "+target)
			in, _ := copyCmd.StdinPipe()
			go func() {
				defer in.Close()
				in.Write(data)
			}()
			copyCmd.Run()

			status.SetText("Status: Berjalan")

			cmdStr := fmt.Sprintf("stty raw -echo; sh %s", target)
			if isBinary {
				cmdStr = target
			}

			cmd := exec.Command("su", "-c", cmdStr)
			cmd.Env = append(os.Environ(), "TERM=xterm-256color")

			stdin, _ = cmd.StdinPipe()
			cmd.Stdout = term
			cmd.Stderr = term

			err := cmd.Run()
			if err != nil {
				term.Write([]byte(fmt.Sprintf("\n\x1b[31m[EXIT ERROR: %v]\x1b[0m\n", err)))
			} else {
				term.Write([]byte("\n\x1b[32m[Selesai]\x1b[0m\n"))
			}
			status.SetText("Status: Selesai")
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
	input.OnSubmitted = func(string) { send() }

	/* UI LAYOUT */
	topControl := container.NewVBox(
		widget.NewButton("Pilih File", func() {
			dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) {
				if r != nil {
					runFile(r)
				}
			}, w).Show()
		}),
		widget.NewButton("Clear Log", func() {
			term.Clear()
		}),
		status,
	)

	bottomControl := container.NewBorder(nil, nil, nil, widget.NewButton("Kirim", send), input)

	// ISI UTAMA
	mainContent := container.NewBorder(
		topControl,
		bottomControl,
		nil, nil,
		term.scroll,
	)

	// WRAP UTAMA DALAM INTERACTION LAYER
	// Ini kuncinya: Membungkus seluruh tampilan dengan widget yang bisa di-klik & fokus
	rootLayer := NewInteractionLayer(mainContent, confirmExit)

	w.SetContent(rootLayer)

	// Paksa fokus ke rootLayer saat mulai agar tombol Back langsung aktif
	w.Canvas().Focus(rootLayer)

	// Pastikan background hitam (TextGrid transparan soalnya)
	w.Canvas().SetContent(container.NewMax(
		canvas.NewRectangle(theme.BackgroundColor()), // Background warna tema
		rootLayer,
	))

	w.ShowAndRun()
}

