package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Root Executor ARM64")
	myWindow.Resize(fyne.NewSize(600, 400))

	// UI Elements
	outputArea := widget.NewMultiLineEntry()
	outputArea.SetPlaceHolder("Log output akan muncul di sini...")
	
	statusLabel := widget.NewLabel("Status: Siap")

	// Fungsi utama eksekusi
	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		
		// Update UI awal
		statusLabel.SetText("Status: Menyalin file...")
		outputArea.SetText("")

		// 1. Tentukan path aman di folder internal aplikasi
		// Android melarang eksekusi langsung dari /sdcard
		internalPath := filepath.Join(myApp.Storage().RootURI().Path(), "exec_script.sh")

		// 2. Salin file dari Picker ke Internal Storage
		out, err := os.OpenFile(internalPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
		if err != nil {
			outputArea.SetText("Gagal membuat file internal: " + err.Error())
			return
		}
		_, err = io.Copy(out, reader)
		out.Close()

		if err != nil {
			outputArea.SetText("Gagal menyalin script: " + err.Error())
			return
		}

		// 3. Eksekusi di dalam Goroutine agar UI tidak blank/hang
		go func() {
			statusLabel.SetText("Status: Menjalankan (ROOT)...")

			// Menggunakan 'su -c' untuk memanggil akses root
			// Kita panggil 'sh' untuk menjalankan file yang tadi disalin
			cmdString := fmt.Sprintf("sh %s", internalPath)
			cmd := exec.Command("su", "-c", cmdString)

			// Menangkap output (stdout & stderr)
			output, err := cmd.CombinedOutput()

			// Update UI harus kembali ke main thread (Canvas Refresh)
			myWindow.Canvas().Refresh(outputArea)
			
			if err != nil {
				outputArea.SetText(fmt.Sprintf("ERROR:\n%v\n\nOUTPUT:\n%s", err, string(output)))
				statusLabel.SetText("Status: Gagal")
			} else {
				outputArea.SetText(string(output))
				statusLabel.SetText("Status: Selesai")
			}
		}()
	}

	// Tombol Pilih File
	btnSelect := widget.NewButton("Pilih & Jalankan File (.sh/.bin)", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if err != nil {
				dialog.ShowError(err, myWindow)
				return
			}
			if reader == nil {
				return
			}
			executeFile(reader)
		}, myWindow)
		fd.Show()
	})

	// Layouting
	content := container.NewBorder(
		container.NewVBox(btnSelect, statusLabel), 
		nil, nil, nil, 
		outputArea,
	)

	myWindow.SetContent(content)
	myWindow.ShowAndRun()
}

