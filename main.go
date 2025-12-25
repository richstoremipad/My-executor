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
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

/* ==========================================
   1. CUSTOM INPUT (AGAR BACK KEY TERDETEKSI SAAT NGETIK)
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
	// Di Android, Back Button = KeyEscape
	if key.Name == fyne.KeyEscape {
		if e.onBack != nil {
			e.onBack()
		}
		// Return agar tidak lanjut ke default handler (menutup keyboard tanpa exit)
		return 
	}
	e.Entry.TypedKey(key)
}

/* ==========================================
   2. TERMINAL WIDGET
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
	
	// [WAJIB UNTUK ANDROID] 
	// Tanpa ini, Back Button dianggap "Minimize/Home" oleh OS, bukan "Close Window"
	w.SetMaster() 

	/* --- LOGIKA KELUAR AMAN --- */
	confirmExit := func() {
		// Gunakan dialog.ShowConfirm biasa
		dialog.ShowConfirm("Konfirmasi Keluar", "Apakah Anda yakin ingin keluar?", func(ok bool) {
			if ok {
				a.Quit()
			}
		}, w)
	}

	// 1. Intercept Close Request (Saat w.SetMaster aktif, Back akan memicu ini)
	w.SetCloseIntercept(confirmExit)

	// 2. Global Key Listener (Backup jika fokus ada di canvas/background)
	w.Canvas().SetOnTypedKey(func(k *fyne.KeyEvent) {
		if k.Name == fyne.KeyEscape {
			confirmExit()
		}
	})

	/* TERMINAL SETUP */
	term := NewTerminal()

	/* INPUT SETUP */
	// Gunakan Custom Entry agar saat mengetik pun Back bisa dicegat
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
			// PENTING: Inject TERM agar warna keluar
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

	w.SetContent(container.NewBorder(
		topControl,
		bottomControl,
		nil, nil,
		term.scroll,
	))

	w.ShowAndRun()
}

