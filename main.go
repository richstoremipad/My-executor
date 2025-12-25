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
	myWindow := myApp.NewWindow("Executor Pro (APatch)")
	myWindow.Resize(fyne.NewSize(600, 450))

	outputArea := widget.NewMultiLineEntry()
	statusLabel := widget.NewLabel("Status: Ready")

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		
		statusLabel.SetText("Status: Copying file...")
		outputArea.SetText("")

		internalPath := filepath.Join(myApp.Storage().RootURI().Path(), "run.sh")
		out, _ := os.OpenFile(internalPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
		io.Copy(out, reader)
		out.Close()

		go func() {
			statusLabel.SetText("Status: Executing ROOT...")

			// Gunakan shell interaktif untuk menghindari hang pada APatch
			cmd := exec.Command("su")
			
			// Hubungkan stdin agar kita bisa menyuapkan perintah
			stdin, err := cmd.StdinPipe()
			if err != nil {
				statusLabel.SetText("Error: Failed to open stdin")
				return
			}

			// Tangkap output gabungan
			combinedBuf := &customBuffer{area: outputArea, window: myWindow}
			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			// Jalankan perintah su
			if err := cmd.Start(); err != nil {
				outputArea.SetText("Error starting SU: " + err.Error())
				return
			}

			// Suntikkan perintah ke dalam shell root
			// Kita tambah 'exit' di akhir agar shell menutup sendiri setelah selesai
			fmt.Fprintf(stdin, "export PATH=/system/bin:/system/xbin:/vendor/bin:$PATH\n")
			fmt.Fprintf(stdin, "sh %s\n", internalPath)
			fmt.Fprintf(stdin, "exit\n")
			stdin.Close()

			// Tunggu sampai selesai
			err = cmd.Wait()
			
			if err != nil {
				statusLabel.SetText("Status: Finished with Error")
				outputArea.Append("\n[Process Exited with Error]\n")
			} else {
				statusLabel.SetText("Status: Success")
				outputArea.Append("\n[Process Finished]\n")
			}
		}()
	}

	btnSelect := widget.NewButton("Pilih Script", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil { executeFile(reader) }
		}, myWindow)
		fd.Show()
	})

	myWindow.SetContent(container.NewBorder(
		container.NewVBox(btnSelect, statusLabel), nil, nil, nil, outputArea,
	))
	myWindow.ShowAndRun()
}

// customBuffer untuk update teks secara real-time
type customBuffer struct {
	area   *widget.Entry
	window fyne.Window
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	text := string(p)
	cb.area.Append(text)
	cb.window.Canvas().Refresh(cb.area)
	return len(p), nil
}

