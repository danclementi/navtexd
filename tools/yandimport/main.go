// Command yandimport converts logs of YaND to the export format of navtexd (one JSON line per message), which the Log tab's
// Import (Database dialog) reads. Files can also be imported there directly; this tool is for those who prefer the command line,
// or want to look at the result first.
//
//	yandimport [-o out.jsonl] YAND_DB.ABS [NDX20261002_190129.TXT ...]
//
// A YaND database (YAND_DB.ABS) and reception reports (NDX*.TXT) are recognized by what they contain. Without -o the lines go
// to standard output; a summary goes to standard error.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"navtex/internal/store"
	"navtex/internal/yand"
)

func main() {
	out := flag.String("o", "", "write the records to this file (default: standard output)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: yandimport [-o out.jsonl] YAND_DB.ABS|NDXyyyymmdd_hhmmss.TXT ...")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	var all []store.Record
	for _, name := range flag.Args() {
		recs, msg, err := yand.ReadFile(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "%s: %s\n", name, msg)
		all = append(all, recs...)
	}
	w := os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer f.Close()
		w = f
	}
	bw := bufio.NewWriter(w)
	for i := range all {
		all[i].ID = int64(i + 1)
		b, err := json.Marshal(all[i])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		bw.WriteString(`{"op":"put","r":`)
		bw.Write(b)
		bw.WriteString("}\n")
	}
	if err := bw.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "%d records written\n", len(all))
}
