package main

import (
	"bytes"
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
	myWindow := myApp.NewWindow("Root Executor v2")
	myWindow.Resize(fyne.NewSize(600, 400))

	outputArea := widget.NewMultiLineEntry()
	statusLabel := widget.NewLabel("Status: Siap")

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		
		statusLabel.SetText("Status: Menyiapkan Sandbox...")
		outputArea.SetText("Menyalin file ke internal storage...\n")

		// Gunakan folder cache internal aplikasi
		internalPath := filepath.Join(myApp.Storage().RootURI().Path(), "exec_script.sh")

		// Proses Copy
		out, _ := os.OpenFile(internalPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
		io.Copy(out, reader)
		out.Close()

		// Jalankan di Background (Goroutine)
		go func() {
			statusLabel.SetText("Status: Mengeksekusi (ROOT)...")
			
			// Gunakan shell internal secara eksplisit
			// Bungkus dengan sh -c agar lebih stabil di banyak versi Android
			cmdString := fmt.Sprintf("sh %s", internalPath)
			cmd := exec.Command("su", "-c", cmdString)

			// Gunakan buffer untuk menangkap output agar tidak stuck
			var resOut bytes.Buffer
			cmd.Stdout = &resOut
			cmd.Stderr = &resOut

			err := cmd.Run()

			// Update UI
			myWindow.Canvas().Refresh(outputArea)
			
			if err != nil {
				outputArea.SetText(fmt.Sprintf("STATUS: ERROR/STUCK\nLog: %v\n\nOutput Terakhir:\n%s", err, resOut.String()))
				statusLabel.SetText("Status: Gagal")
			} else {
				if resOut.Len() == 0 {
					outputArea.SetText("Script selesai tanpa output teks.")
				} else {
					outputArea.SetText(resOut.String())
				}
				statusLabel.SetText("Status: Berhasil")
			}
		}()
	}

	btnSelect := widget.NewButton("Pilih Script .sh", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil {
				executeFile(reader)
			}
		}, myWindow)
		fd.Show()
	})

	btnClear := widget.NewButton("Bersihkan Log", func() {
		outputArea.SetText("")
		statusLabel.SetText("Status: Siap")
	})

	myWindow.SetContent(container.NewBorder(
		container.NewVBox(btnSelect, btnClear, statusLabel), nil, nil, nil, outputArea,
	))
	myWindow.ShowAndRun()
}

