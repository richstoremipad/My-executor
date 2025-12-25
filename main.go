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
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

/* ==========================================
   TERMINAL LOGIC (Jantung Aplikasi)
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
	case "90": return color.Gray{Y: 150}
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
              MAIN UI
================================ */

func main() {
	a := app.New()
	a.Settings().SetTheme(theme.DarkTheme())

	w := a.NewWindow("Root Executor")
	w.Resize(fyne.NewSize(720, 520))
	w.SetMaster()

	term := NewTerminal()
	input := widget.NewEntry()
	input.SetPlaceHolder("Ketik perintah...")
	status := widget.NewLabel("Status: Siap")
	status.TextStyle = fyne.TextStyle{Bold: true}
	var stdin io.WriteCloser

	/* --- FUNGSI EKSEKUSI --- */
	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		term.Clear()
		status.SetText("Status: Menyiapkan...")
		data, _ := io.ReadAll(reader)
		target := "/data/local/tmp/temp_exec"
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))
		go func() {
			exec.Command("su", "-c", "rm -f "+target).Run()
			copyCmd := exec.Command("su", "-c", "cat > "+target+" && chmod 777 "+target)
			in, _ := copyCmd.StdinPipe()
			go func() {
				defer in.Close()
				in.Write(data)
			}()
			copyCmd.Run()
			status.SetText("Status: Berjalan")
			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", target)
			} else {
				cmd = exec.Command("su", "-c", "sh "+target)
			}
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

	/* ==========================================
	   UI CONSTRUCTION (DESIGN FINAL)
	========================================== */

	// 1. HEADER (Hanya Judul & Clear)
	titleText := canvas.NewText("ROOT EXECUTOR", theme.ForegroundColor())
	titleText.TextSize = 16
	titleText.TextStyle = fyne.TextStyle{Bold: true}

	clearBtn := widget.NewButtonWithIcon("", theme.ContentClearIcon(), func() {
		term.Clear()
	})
	
	// Layout Header
	headerBar := container.NewBorder(nil, nil, 
		container.NewPadded(titleText), // Kiri
		clearBtn, // Kanan
	)

	topSection := container.NewVBox(
		headerBar,
		container.NewPadded(status),
		widget.NewSeparator(),
	)

	// 2. COPYRIGHT STYLE (KEREN & BERWARNA)
	// Kita gunakan canvas.NewText agar bisa set warna Hex spesifik
	// Warna: Neon Pink/Magenta (RGB: 255, 0, 128)
	copyright := canvas.NewText("Made by TANGSAN", color.RGBA{R: 255, G: 0, B: 128, A: 255})
	copyright.TextSize = 10
	copyright.Alignment = fyne.TextAlignCenter
	copyright.TextStyle = fyne.TextStyle{Bold: true, Monospace: true} // Font Hacker style

	// 3. INPUT BAR (BAWAH)
	sendBtn := widget.NewButtonWithIcon("Kirim", theme.MailSendIcon(), send)
	inputContainer := container.NewPadded(
		container.NewBorder(nil, nil, nil, sendBtn, input),
	)

	// Gabungkan Copyright & Input dalam satu container bawah
	// Copyright ditaruh DI ATAS Input
	bottomSection := container.NewVBox(
		copyright,
		inputContainer,
	)

	// 4. MAIN CONTENT LAYER (Terminal di Tengah)
	mainLayer := container.NewBorder(
		topSection,    // Atas
		bottomSection, // Bawah (Copyright + Input)
		nil, nil,
		term.scroll,   // Tengah
	)

	// 5. FLOATING ACTION BUTTON (FAB)
	fabBtn := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) {
			if r != nil {
				runFile(r)
			}
		}, w).Show()
	})
	fabBtn.Importance = widget.HighImportance

	// Atur posisi FAB agar tidak menutupi Input Bar
	// Kita beri spacer bawah lebih banyak agar naik sedikit di atas input bar
	fabContainer := container.NewVBox(
		layout.NewSpacer(), 
		container.NewHBox(
			layout.NewSpacer(),
			fabBtn,
			widget.NewLabel(" "), // Margin Kanan
		),
		widget.NewLabel("      "), // Margin Bawah (Spacer dummy text)
		widget.NewLabel("      "), // Tambahan Margin Bawah agar di atas input
	)

	// 6. STACK LAYOUT
	finalLayout := container.NewStack(mainLayer, fabContainer)

	w.SetContent(finalLayout)
	w.ShowAndRun()
}

