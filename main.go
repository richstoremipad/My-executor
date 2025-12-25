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
   TERMINAL WIDGET (TextGrid + ANSI Color Fix)
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

	// Default Style: Teks Putih/Abu, Background Transparan
	defStyle := &widget.CustomTextGridStyle{
		FGColor: theme.ForegroundColor(),
		BGColor: color.Transparent,
	}

	// Regex untuk menangkap kode ANSI (Warna & Kontrol)
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

// Map Kode Warna ANSI agar mirip MT Manager
func ansiToColor(code string) color.Color {
	switch code {
	// Standard Colors (30-37)
	case "30": return color.Gray{Y: 100}                 // Black/Dark Grey
	case "31": return theme.ErrorColor()                 // Red
	case "32": return theme.SuccessColor()               // Green
	case "33": return theme.WarningColor()               // Yellow/Orange
	case "34": return theme.PrimaryColor()               // Blue
	case "35": return color.RGBA{200, 0, 200, 255}       // Magenta
	case "36": return color.RGBA{0, 255, 255, 255}       // Cyan (XFILES Blue)
	case "37": return theme.ForegroundColor()            // White

	// Bright/Bold Colors (90-97) - Sering dipakai MT Manager
	case "90": return color.Gray{Y: 150}                 // Bright Grey
	case "91": return color.RGBA{255, 100, 100, 255}     // Bright Red
	case "92": return color.RGBA{100, 255, 100, 255}     // Bright Green
	case "93": return color.RGBA{255, 255, 100, 255}     // Bright Yellow
	case "94": return color.RGBA{100, 100, 255, 255}     // Bright Blue
	case "95": return color.RGBA{255, 100, 255, 255}     // Bright Magenta
	case "96": return color.RGBA{100, 255, 255, 255}     // Bright Cyan
	case "97": return color.White                        // Bright White

	// Kode "1" (Bold) kita return nil agar TIDAK mereset warna yang sudah ada
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

		// Cetak teks sebelum kode ANSI
		if loc[0] > 0 {
			t.printText(raw[:loc[0]])
		}

		// Proses kode ANSI
		ansiCode := raw[loc[0]:loc[1]]
		t.handleAnsiCode(ansiCode)

		// Lanjut ke sisa string
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
	case 'm': // Ganti Warna
		parts := strings.Split(content, ";")
		for _, part := range parts {
			if part == "" || part == "0" {
				// Reset ke default
				t.curStyle.FGColor = theme.ForegroundColor()
			} else {
				// Cek warna
				col := ansiToColor(part)
				if col != nil {
					// HANYA ganti jika col tidak nil.
					// Ini mencegah kode "1" (Bold) mengubah warna jadi putih.
					t.curStyle.FGColor = col
				}
			}
		}
	case 'J': // Clear Screen
		if strings.Contains(content, "2") {
			t.Clear()
		}
	case 'H': // Cursor Home
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

		// Expand Rows
		for t.curRow >= len(t.grid.Rows) {
			t.grid.SetRow(len(t.grid.Rows), widget.TextGridRow{Cells: []widget.TextGridCell{}})
		}

		// Expand Cols
		rowCells := t.grid.Rows[t.curRow].Cells
		if t.curCol >= len(rowCells) {
			newCells := make([]widget.TextGridCell, t.curCol+1)
			copy(newCells, rowCells)
			t.grid.SetRow(t.curRow, widget.TextGridRow{Cells: newCells})
		}

		// Copy style value agar warna "menempel" di karakter tersebut
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

	/* TERMINAL SETUP */
	term := NewTerminal()

	input := widget.NewEntry()
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
			
			// Inject TERM agar script mau mengeluarkan warna
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

	/* UI LAYOUT (Original) */
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

