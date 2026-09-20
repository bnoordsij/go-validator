package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"github.com/syumai/workers-go"
)

func main() {

	http.HandleFunc("/status", statusHandler)
	http.HandleFunc("/loopback", loopbackHandler)
	http.HandleFunc("/validate", validateHandler)
	http.HandleFunc("/output", outputHandler)

        http.HandleFunc("/tst-example", func(w http.ResponseWriter, req *http.Request) {
                out, err := os.Create("output.txt")
		check(err)
		defer out.Close()

                resp, err := http.Get("http://example.com/")
		check(err)
                defer resp.Body.Close()

                io.Copy(out, resp.Body)

        })

	http.HandleFunc("/hello", func(w http.ResponseWriter, req *http.Request) {
		msg := "Hello!"
		w.Write([]byte(msg))
	})
	http.HandleFunc("/echo", func(w http.ResponseWriter, req *http.Request) {
		b, err := io.ReadAll(req.Body)
		check(err)
		io.Copy(w, bytes.NewReader(b))
	})
	statusUrl := os.Getenv("STATUS_URL")
	if (statusUrl != "")  {
		resp, err := http.Get(statusUrl)
		check(err)
		resp.Body.Close()
	}
	touchFilename := "touch.txt"
	os.OpenFile(touchFilename, os.O_RDONLY|os.O_CREATE, 0666)

	fmt.Print("Running\r\n")
	workers.Serve(nil) // use http.DefaultServeMux
}
