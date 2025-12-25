package main

import (
	"bytes"
	"fmt"
	"image/color"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// --- KONFIGURASI ---
// TextGrid otomatis menggunakan font Monospace bawaan Fyne.

// --- STRUKTUR TERMINAL ---
type VirtualTerminal struct {
	grid       *widget.TextGrid
	scroll     *container.Scroll
	mutex      sync.Mutex
	
	// Posisi Kursor
	row int
	col int
	
	// Style Saat Ini
	currentStyle widget.TextGridStyle
}

// Map Warna ANSI Standar
var ansiColors = map[int]color.Color{
	30: color.RGBA{128, 128, 128, 255}, // Black/Gray
	31: color.RGBA{255, 80, 80, 255},   // Red
	32: color.RGBA{80, 200, 80, 255},   // Green
	33: color.RGBA{255, 255, 80, 255},  // Yellow
	34: color.RGBA{100, 150, 255, 255}, // Blue
	35: color.RGBA{255, 100, 255, 255}, // Magenta
	36: color.RGBA{100, 255, 255, 255}, // Cyan
	37: color.White,                    // White
	90: color.RGBA{100, 100, 100, 255}, // Bright Black
	91: color.RGBA{255, 0, 0, 255},     // Bright Red
	92: color.RGBA{0, 255, 0, 255},     // Bright Green
	93: color.RGBA{255, 255, 0, 255},   // Bright Yellow
	94: color.RGBA{92, 92, 255, 255},   // Bright Blue
	95: color.RGBA{255, 0, 255, 255},   // Bright Magenta
	96: color.RGBA{0, 255, 255, 255},   // Bright Cyan
	97: color.White,                    // Bright White
}

// --- FUNGSI PARSING & RENDER ---

func NewVirtualTerminal() *VirtualTerminal {
	// Membuat Grid Kosong
	g := widget.NewTextGrid()
	g.ShowLineNumbers = false // Matikan nomor baris agar mirip terminal
	
	// Setup container scroll
	s := container.NewScroll(g)
	
	return &VirtualTerminal{
		grid:   g,
		scroll: s,
		row:    0,
		col:    0,
		currentStyle: widget.TextGridStyle{
			FGColor: color.White, // Default text putih
			BGColor: color.Transparent,
		},
	}
}

// Reset layar (Clear Screen)
func (vt *VirtualTerminal) Clear() {
	vt.mutex.Lock()
	defer vt.mutex.Unlock()
	
	// Cara reset TextGrid: Buat baris baru kosong
	vt.grid.SetText("") 
	vt.row = 0
	vt.col = 0
	vt.grid.Refresh()
}

// Fungsi inti untuk menulis byte stream ke Grid
func (vt *VirtualTerminal) Write(p []byte) (n int, err error) {
	vt.mutex.Lock()
	defer vt.mutex.Unlock()

	text := string(p)
	
	// Regex untuk memisahkan Kode ANSI dan Teks Biasa
	// Menangkap: \x1b [ angka... huruf
	re := regexp.MustCompile(`(\x1b\[[0-9;?]*[a-zA-Z])`)
	
	parts := re.Split(text, -1)
	matches := re.FindAllString(text, -1)
	
	// Iterasi bagian teks dan kode secara berurutan
	// Karena Split dan FindAll terpisah, kita perlu logika merge sederhana
	// Namun untuk terminal stream, kita bisa parse linear scanning karakter per karakter
	// atau menggunakan pendekatan split regex ini (dengan asumsi urutan terjaga).
	
	// PENDEKATAN LINEAR SCANNING (Lebih Aman untuk urutan)
	// Kita akan memproses 'text' secara manual untuk menangani escape sequence
	
	i := 0
	runes := []rune(text)
	lenRunes := len(runes)

	for i < lenRunes {
		char := runes[i]

		if char == '\x1b' {
			// Deteksi awal Escape Sequence
			endIdx := i + 1
			for endIdx < lenRunes {
				c := runes[endIdx]
				// Karakter akhir ANSI biasanya huruf (m, J, H, K, dll)
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
					endIdx++
					break
				}
				endIdx++
			}
			
			ansiCode := string(runes[i:endIdx])
			vt.handleAnsiCode(ansiCode)
			i = endIdx
			continue
		}

		// Handle Newline
		if char == '\n' {
			vt.row++
			vt.col = 0
			i++
			continue
		}
		
		// Handle Carriage Return (Reset ke awal baris)
		if char == '\r' {
			vt.col = 0
			i++
			continue
		}
		
		// Handle Tab (4 spasi)
		if char == '\t' {
			for k := 0; k < 4; k++ {
				vt.setCell(' ')
			}
			i++
			continue
		}

		// Karakter Biasa: Tulis ke Grid
		vt.setCell(char)
		i++
	}

	vt.grid.Refresh()
	vt.scroll.ScrollToBottom()
	return len(p), nil
}

func (vt *VirtualTerminal) setCell(r rune) {
	// Pastikan baris tersedia
	for len(vt.grid.Rows) <= vt.row {
		vt.grid.Rows = append(vt.grid.Rows, widget.TextGridRow{})
	}
	
	// Ambil baris saat ini
	 currentRow := vt.grid.Rows[vt.row]
	 
	 // Pastikan kolom tersedia (isi spasi jika loncat)
	 for len(currentRow.Cells) <= vt.col {
	 	currentRow.Cells = append(currentRow.Cells, widget.TextGridCell{Rune: ' '})
	 }
	 
	 // Set karakter dan style di posisi kursor
	 currentRow.Cells[vt.col] = widget.TextGridCell{
	 	Rune:  r,
	 	Style: vt.currentStyle,
	 }
	 
	 // Simpan kembali perubahan baris
	 vt.grid.Rows[vt.row] = currentRow
	 
	 // Majukan kursor
	 vt.col++
}

func (vt *VirtualTerminal) handleAnsiCode(code string) {
	// Hapus prefix \x1b[ dan suffix huruf untuk ambil parameter
	if len(code) < 3 { return }
	
	cmd := code[len(code)-1] // Huruf terakhir (m, J, H, dll)
	paramStr := code[2 : len(code)-1]
	
	switch cmd {
	case 'm': // Ganti Warna/Style
		parts := strings.Split(paramStr, ";")
		for _, p := range parts {
			val, _ := strconv.Atoi(p)
			vt.updateStyle(val)
		}
	case 'J': // Clear Screen
		if strings.Contains(paramStr, "2") {
			vt.Clear()
		}
	// Case H (Home) bisa diabaikan atau reset col/row, tapi vt.Clear() biasanya sudah cukup
	}
}

func (vt *VirtualTerminal) updateStyle(code int) {
	if code == 0 {
		// Reset
		vt.currentStyle.FGColor = color.White
		vt.currentStyle.BGColor = color.Transparent
	} else if code >= 30 && code <= 37 {
		// Foreground Standard
		vt.currentStyle.FGColor = ansiColors[code]
	} else if code >= 90 && code <= 97 {
		// Foreground Bright
		vt.currentStyle.FGColor = ansiColors[code]
	} else if code >= 40 && code <= 47 {
		// Background Standard (Map 40->30)
		vt.currentStyle.BGColor = ansiColors[code-10]
	}
}

// --- MAIN PROGRAM ---

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Universal Terminal")
	myWindow.Resize(fyne.NewSize(800, 600))

	// 1. Inisialisasi Terminal Kustom
	term := NewVirtualTerminal()
	
	// Background Hitam untuk Terminal Container
	// TextGrid transparan secara default, jadi kita butuh background di belakangnya
	bgRect := container.NewStack(
		// Ganti warna background sesuai selera (Hitam Pekat)
		widget.NewButton("", nil), // Hack simple untuk background atau gunakan canvas
	)
	// Lebih bersih pakai canvas rectangle:
	realBg := &fyne.Container{
		Objects: []fyne.CanvasObject{
			// Background Hitam Full
			// Menggunakan rectangle hitam
			// Note: Kita tumpuk TextGrid di atas background hitam
		},
	}
	_ = realBg // Skip complex layout, TextGrid biasanya sudah oke.
	
	// Langsung gunakan term.scroll. TextGrid default backgroundnya mengikuti tema, 
	// tapi karena kita set style Text putih, sebaiknya tema aplikasi gelap.
	
	// 2. INPUT AREA
	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik perintah...")
	
	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	// 3. EXECUTE LOGIC
	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Loading...")
		term.Clear() // Bersihkan layar terminal

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			// Setup File
			exec.Command("su", "-c", "rm "+targetPath).Run()
			copyCmd := exec.Command("su", "-c", "cat > "+targetPath+" && chmod 755 "+targetPath)
			copyStdin, _ := copyCmd.StdinPipe()
			go func() {
				defer copyStdin.Close()
				copyStdin.Write(data)
			}()
			copyCmd.Run()

			statusLabel.SetText("Running...")

			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}

			// ENV PENTING untuk TextGrid
			cmd.Env = append(os.Environ(),
				"PATH=/system/bin:/system/xbin:/vendor/bin:/data/local/tmp",
				"TERM=xterm-256color",
				"COLUMNS=80", // Lebar terminal standar
				"LINES=25",
			)

			stdinPipe, _ = cmd.StdinPipe()

			// Hubungkan Stdout/Stderr ke Terminal Kustom kita
			cmd.Stdout = term
			cmd.Stderr = term

			err := cmd.Run()
			if err != nil {
				statusLabel.SetText("Exit Code: " + err.Error())
			} else {
				statusLabel.SetText("Done")
			}
			stdinPipe = nil
		}()
	}

	sendInput := func() {
		if stdinPipe != nil && inputEntry.Text != "" {
			fmt.Fprintf(stdinPipe, "%s\n", inputEntry.Text)
			inputEntry.SetText("")
		}
	}

	inputEntry.OnSubmitted = func(s string) { sendInput() }
	btnSend := widget.NewButton("Kirim", func() { sendInput() })
	btnSelect := widget.NewButton("Load File", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil { executeFile(reader) }
		}, myWindow)
		fd.Show()
	})

	// Layout Akhir
	controls := container.NewVBox(btnSelect, statusLabel)
	bottomBar := container.NewBorder(nil, nil, nil, btnSend, inputEntry)
	
	// Gunakan term.scroll (TextGrid) di tengah
	content := container.NewBorder(controls, bottomBar, nil, nil, term.scroll)

	myWindow.SetContent(content)
	myWindow.ShowAndRun()
}

