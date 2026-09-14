package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"github.com/syumai/workers-go"
)

func main() {

	http.HandleFunc("/status", statusHandler)
	http.HandleFunc("/validate", validateHandler)

        http.HandleFunc("/tst-example", func(w http.ResponseWriter, req *http.Request) {
                out, err := os.Create("output.txt")
		if err != nil {
 			panic(err)
		}
		defer out.Close()

                resp, err := http.Get("http://example.com/")
		if err != nil {
 			panic(err)
		}
                defer resp.Body.Close()

                io.Copy(out, resp.Body)

        })

	http.HandleFunc("/hello", func(w http.ResponseWriter, req *http.Request) {
		msg := "Hello!"
		w.Write([]byte(msg))
	})
	http.HandleFunc("/echo", func(w http.ResponseWriter, req *http.Request) {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			panic(err)
		}
		io.Copy(w, bytes.NewReader(b))
	})
	workers.Serve(nil) // use http.DefaultServeMux
}
