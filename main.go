package main

import (
	"bytes"
	"fmt"
	"image/color"
	"io"
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
   TERMINAL WIDGET (TEXTGRID + ANSI REGEX)
========================================== */

type Terminal struct {
	grid     *widget.TextGrid
	scroll   *container.Scroll
	curRow   int
	curCol   int
	curStyle *widget.CustomTextGridStyle
	mutex    sync.Mutex
	reAnsi   *regexp.Regexp // Regex untuk menangkap semua kode ANSI
}

func NewTerminal() *Terminal {
	g := widget.NewTextGrid()
	g.ShowLineNumbers = false // Hilangkan nomor baris biar bersih
	
	// Default Style: Teks mengikuti tema, Background Transparan
	defStyle := &widget.CustomTextGridStyle{
		FGColor: theme.ForegroundColor(),
		BGColor: color.Transparent,
	}

	// Regex ini menangkap ESC + [ + angka + huruf perintah
	// Contoh: \x1b[31m (Merah), \x1b[2J (Clear Screen)
	// regex: ESC + [ + (parameter opsional) + (huruf command)
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

// Konversi Kode ANSI ke Warna Fyne
func ansiToColor(code string) color.Color {
	switch code {
	case "30", "90": return color.Gray{Y: 100}
	case "31", "91": return theme.ErrorColor()   // Merah
	case "32", "92": return theme.SuccessColor() // Hijau
	case "33", "93": return theme.WarningColor() // Kuning
	case "34", "94": return theme.PrimaryColor() // Biru
	case "35", "95": return color.RGBA{R: 180, G: 0, B: 180, A: 255} // Ungu
	case "36", "96": return theme.PrimaryColor() // Cyan (Biasanya dipakai untuk XFILES)
	case "37", "97": return theme.ForegroundColor() // Putih
	case "1": return theme.ForegroundColor() // Bold (anggap putih terang)
	default: return theme.ForegroundColor()
	}
}

func (t *Terminal) Clear() {
	t.grid.SetText("") // Kosongkan grid
	t.curRow = 0
	t.curCol = 0
}

// Fungsi Write dengan Parser Regex yang Kuat
func (t *Terminal) Write(p []byte) (int, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	raw := string(p)
	raw = strings.ReplaceAll(raw, "\r\n", "\n") // Normalisasi enter

	// Loop parsing string
	for len(raw) > 0 {
		// Cari posisi kode ANSI berikutnya
		loc := t.reAnsi.FindStringIndex(raw)

		if loc == nil {
			// Tidak ada kode ANSI lagi, cetak sisa teks apa adanya
			t.printText(raw)
			break
		}

		// Ada kode ANSI. 
		// 1. Cetak teks biasa SEBELUM kode ANSI (jika ada)
		if loc[0] > 0 {
			t.printText(raw[:loc[0]])
		}

		// 2. Proses Kode ANSI-nya
		ansiCode := raw[loc[0]:loc[1]] // Contoh string: "\x1b[31m" atau "\x1b[2J"
		t.handleAnsiCode(ansiCode)

		// 3. Potong string dan ulangi loop untuk sisanya
		raw = raw[loc[1]:]
	}

	t.grid.Refresh()
	t.scroll.ScrollToBottom()
	return len(p), nil
}

// Logika menangani kode ANSI (Warna & Clear Screen)
func (t *Terminal) handleAnsiCode(codeSeq string) {
	// Hapus prefix "\x1b[" dan ambil huruf terakhir
	// Contoh: "\x1b[31m" -> content="31", command='m'
	if len(codeSeq) < 3 { return }
	
	content := codeSeq[2 : len(codeSeq)-1]
	command := codeSeq[len(codeSeq)-1] // Huruf terakhir (m, J, H, K, dll)

	switch command {
	case 'm': // 'm' = Ganti Warna (Graphics Mode)
		parts := strings.Split(content, ";")
		for _, part := range parts {
			if part == "" || part == "0" {
				t.curStyle.FGColor = theme.ForegroundColor() // Reset ke default
			} else {
				t.curStyle.FGColor = ansiToColor(part)
			}
		}
	case 'J': // 'J' = Clear Screen commands
		// [2J = Clear entire screen
		if strings.Contains(content, "2") {
			t.Clear() 
		}
	case 'H': // 'H' = Cursor Home (Pindah ke pojok kiri atas)
		t.curRow = 0
		t.curCol = 0
	// Kode lain (seperti K, A, B) diabaikan agar tidak jadi sampah simbol
	}
}

// Fungsi internal mencetak karakter ke Grid satu per satu
func (t *Terminal) printText(text string) {
	for _, char := range text {
		if char == '\n' {
			t.curRow++
			t.curCol = 0
			continue
		}
		if char == '\r' {
			t.curCol = 0 // Overwrite baris (untuk loading bar)
			continue
		}

		// Tambah baris jika kurang
		for t.curRow >= len(t.grid.Rows) {
			t.grid.SetRow(len(t.grid.Rows), widget.TextGridRow{Cells: []widget.TextGridCell{}})
		}

		// Tambah kolom (cell) jika kurang
		rowCells := t.grid.Rows[t.curRow].Cells
		if t.curCol >= len(rowCells) {
			newCells := make([]widget.TextGridCell, t.curCol+1)
			copy(newCells, rowCells)
			t.grid.SetRow(t.curRow, widget.TextGridRow{Cells: newCells})
		}

		// Set huruf dengan warna yang sedang aktif
		t.grid.SetCell(t.curRow, t.curCol, widget.TextGridCell{
			Rune:  char,
			Style: t.curStyle,
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

	/* TERMINAL */
	term := NewTerminal()

	/* INPUT */
	input := widget.NewEntry()
	input.SetPlaceHolder("Ketik perintah...")

	status := widget.NewLabel("Status: Siap")
	status.TextStyle = fyne.TextStyle{Bold: true}

	var stdin io.WriteCloser

	/* RUN FILE */
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

			var cmd *exec.Cmd
			// stty raw -echo penting agar layout tidak berantakan
			cmdStr := fmt.Sprintf("stty raw -echo; sh %s", target)
			if isBinary {
				cmdStr = target
			}

			cmd = exec.Command("su", "-c", cmdStr)

			stdin, _ = cmd.StdinPipe()
			cmd.Stdout = term
			cmd.Stderr = term

			err := cmd.Run()
			
			if err != nil {
				// Tulis error dengan warna Merah manual (\x1b[31m)
				term.Write([]byte(fmt.Sprintf("\n\x1b[31m[EXIT ERROR: %v]\x1b[0m\n", err)))
			} else {
				// Tulis sukses dengan warna Hijau manual (\x1b[32m)
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
			// Tampilkan input user dengan warna Cyan (\x1b[36m)
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

