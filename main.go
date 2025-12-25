package main

import (
	"bytes"
	"fmt"
	"image/color"
	"io"
	"os/exec"
	"strconv"
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
   TERMINAL WIDGET (PENGGANTI RICHTEXT)
   Menggunakan TextGrid agar 100% Rapat & Presisi
========================================== */

type Terminal struct {
	grid       *widget.TextGrid
	scroll     *container.Scroll
	curRow     int
	curCol     int
	curStyle   *widget.CustomTextGridStyle
	mutex      sync.Mutex
}

func NewTerminal() *Terminal {
	g := widget.NewTextGrid()
	g.ShowLineNumbers = false // Hilangkan nomor baris biar murni terminal
	
	// Style default: Putih (Foreground), Background Transparan
	defStyle := &widget.CustomTextGridStyle{
		FGColor: theme.ForegroundColor(),
		BGColor: color.Transparent,
	}

	return &Terminal{
		grid:     g,
		scroll:   container.NewScroll(g),
		curRow:   0,
		curCol:   0,
		curStyle: defStyle,
	}
}

// Map kode ANSI ke Warna Fyne
func ansiToColor(code string) color.Color {
	switch code {
	case "30", "90": return color.Gray{Y: 100} // Grey
	case "31", "91": return theme.ErrorColor() // Red
	case "32", "92": return theme.SuccessColor() // Green
	case "33", "93": return theme.WarningColor() // Yellow/Orange
	case "34", "94": return theme.PrimaryColor() // Blue
	case "35", "95": return color.RGBA{R: 200, G: 0, B: 200, A: 255} // Purple
	case "36", "96": return theme.PrimaryColor() // Cyan (mirip Blue di tema gelap)
	case "37", "97": return theme.ForegroundColor() // White
	default: return theme.ForegroundColor()
	}
}

// Fungsi utama untuk menulis ke terminal
func (t *Terminal) Write(p []byte) (int, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	raw := string(p)
	
	// Normalisasi Newline
	raw = strings.ReplaceAll(raw, "\r\n", "\n")

	// Split berdasarkan kode ESC ANSI
	// Contoh: "Text \033[31m Merah" -> ["Text ", "[31m Merah"]
	parts := strings.Split(raw, "\x1b")

	for i, part := range parts {
		content := part
		
		// Jika bukan bagian pertama, berarti ini diawali kode ANSI
		if i > 0 {
			if strings.HasPrefix(content, "[") {
				// Cari akhir kode 'm'
				if idx := strings.Index(content, "m"); idx != -1 {
					codeStr := content[1:idx] // Ambil angka, misal "31;1"
					textPart := content[idx+1:] // Ambil teks setelah kode

					// Parse warna sederhana (ambil kode terakhir)
					codes := strings.Split(codeStr, ";")
					for _, c := range codes {
						if c == "0" {
							t.curStyle.FGColor = theme.ForegroundColor()
						} else {
							t.curStyle.FGColor = ansiToColor(c)
						}
					}
					content = textPart
				}
			}
		}

		// Proses karakter per karakter untuk TextGrid
		for _, char := range content {
			if char == '\n' {
				t.curRow++
				t.curCol = 0
				continue
			}
			if char == '\r' {
				t.curCol = 0 // Overwrite baris yang sama (Fix Loading Bar)
				continue
			}

			// Pastikan baris tersedia
			for t.curRow >= len(t.grid.Rows) {
				t.grid.SetRow(len(t.grid.Rows), []widget.TextGridCell{})
			}
			
			// Pastikan baris cukup panjang
			rowCells := t.grid.Rows[t.curRow].Cells
			if t.curCol >= len(rowCells) {
				// Extend row manual jika perlu (TextGrid biasanya auto, tapi aman manual)
				newCells := make([]widget.TextGridCell, t.curCol+1)
				copy(newCells, rowCells)
				t.grid.SetRow(t.curRow, newCells)
			}

			// Set Cell
			t.grid.SetCell(t.curRow, t.curCol, widget.TextGridCell{
				Rune:  char,
				Style: t.curStyle,
			})
			t.curCol++
		}
	}

	t.grid.Refresh()
	t.scroll.ScrollToBottom()
	return len(p), nil
}

func (t *Terminal) Clear() {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	t.grid.SetContent("") // Reset Grid
	t.curRow = 0
	t.curCol = 0
}

/* ===============================
              MAIN
================================ */

func main() {
	a := app.New()
	a.Settings().SetTheme(theme.DarkTheme())

	w := a.NewWindow("Universal Root Executor")
	w.Resize(fyne.NewSize(720, 520))

	/* --- GANTI RICHTEXT DENGAN TERMINAL CUSTOM --- */
	term := NewTerminal()
	
	/* INPUT */
	input := widget.NewEntry()
	input.SetPlaceHolder("Ketik perintah...")

	status := widget.NewLabel("Status: Siap")
	status.TextStyle = fyne.TextStyle{Bold: true}

	var stdin io.WriteCloser

	/* EXEC FILE FUNCTION */
	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()

		term.Clear()
		status.SetText("Status: Menyiapkan...")

		data, _ := io.ReadAll(reader)
		target := "/data/local/tmp/temp_exec"
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			// Copy File
			copyCmd := exec.Command("su", "-c", "cat > "+target+" && chmod 777 "+target)
			in, _ := copyCmd.StdinPipe()
			go func() {
				defer in.Close()
				in.Write(data)
			}()
			copyCmd.Run()

			status.SetText("Status: Berjalan")

			var cmd *exec.Cmd
			// Gunakan 'sh' agar script berjalan di shell env yang benar
			// Tambahkan 'stty -echo' agar input user tidak muncul double
			cmdStr := fmt.Sprintf("stty -echo; sh %s", target)
			if isBinary {
				cmdStr = target
			}

			cmd = exec.Command("su", "-c", cmdStr)

			stdin, _ = cmd.StdinPipe()
			
			// Hubungkan Stdout/Stderr ke Terminal TextGrid kita
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

	/* SEND INPUT */
	send := func() {
		if stdin != nil && input.Text != "" {
			fmt.Fprintln(stdin, input.Text)
			
			// Tulis input user ke log (Warna Biru)
			// \x1b[36m adalah Cyan/Biru Terang
			term.Write([]byte(fmt.Sprintf("\x1b[36m> %s\x1b[0m\n", input.Text)))
			
			input.SetText("")
		}
	}
	input.OnSubmitted = func(string) { send() }

	/* UI LAYOUT (TOMBOL DI ATAS) */
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
		term.scroll,   // Widget TextGrid dalam Scroll
	))

	w.ShowAndRun()
}

