package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// customBuffer digunakan untuk menangkap output dan memperbarui UI secara real-time
type customBuffer struct {
	area   *widget.Entry
	window fyne.Window
}

// Fungsi untuk menghapus kode warna ANSI (seperti [1;36m) agar teks bersih
func cleanAnsi(str string) string {
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	return ansi.ReplaceAllString(str, "")
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	// Bersihkan teks dari kode warna sebelum ditampilkan
	cleanText := cleanAnsi(string(p))
	cb.area.Append(cleanText)
	// Paksa UI untuk refresh agar log mengalir ke bawah
	cb.window.Canvas().Refresh(cb.area)
	return len(p), nil
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Root Executor Pro (Stabil)")
	myWindow.Resize(fyne.NewSize(600, 500))

	// UI Elements
	outputArea := widget.NewMultiLineEntry()
	outputArea.TextStyle = fyne.TextStyle{Monospace: true} // Gunakan font terminal
	
	statusLabel := widget.NewLabel("Status: Siap")

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		
		statusLabel.SetText("Status: Menyiapkan file...")
		outputArea.SetText("") // Bersihkan log lama setiap kali mulai

		// Simpan ke folder internal agar bisa dieksekusi
		internalPath := filepath.Join(myApp.Storage().RootURI().Path(), "temp_script.sh")
		out, _ := os.OpenFile(internalPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
		io.Copy(out, reader)
		out.Close()

		go func() {
			statusLabel.SetText("Status: Mengeksekusi ROOT...")

			// Gunakan 'su -c' dengan perintah tunggal untuk mencegah looping shell
			// TERM=dumb memberitahu sistem bahwa kita tidak mendukung terminal interaktif
			cmd := exec.Command("su", "-c", "sh "+internalPath)
			cmd.Env = []string{
				"PATH=/system/bin:/system/xbin:/vendor/bin:/product/bin",
				"TERM=dumb",
				"HOME=/data/local/tmp",
			}

			// Gunakan custom buffer untuk output real-time
			combinedBuf := &customBuffer{area: outputArea, window: myWindow}
			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			// Jalankan perintah
			err := cmd.Run()
			
			if err != nil {
				statusLabel.SetText("Status: Selesai dengan Error")
				outputArea.Append("\n[Proses Berhenti: " + err.Error() + "]\n")
			} else {
				statusLabel.SetText("Status: Selesai")
				outputArea.Append("\n[Proses Selesai]\n")
			}
		}()
	}

	// Buttons
	btnSelect := widget.NewButton("Pilih & Jalankan Script", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil {
				executeFile(reader)
			}
		}, myWindow)
		fd.Show()
	})

	btnClear := widget.NewButton("Bersihkan Layar", func() {
		outputArea.SetText("")
		statusLabel.SetText("Status: Siap")
	})

	// Layout
	myWindow.SetContent(container.NewBorder(
		container.NewVBox(btnSelect, btnClear, statusLabel), 
		nil, nil, nil, 
		outputArea,
	))

	myWindow.ShowAndRun()
}

