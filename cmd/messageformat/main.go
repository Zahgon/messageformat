// SPDX-License-Identifier: Apache-2.0
// Ported from the MessageFormat 2 package; see mf2/messageformat/LICENSE.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/Zahgon/messageformat/internal/mf"
)

func main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 4096), 4<<20)
	out := bufio.NewWriter(os.Stdout)
	enc := json.NewEncoder(out)
	status := 0
	for in.Scan() {
		var req mf.Request
		if err := json.Unmarshal(in.Bytes(), &req); err != nil {
			res := mf.InvalidInput(err)
			if err := enc.Encode(res); err != nil {
				fmt.Fprintln(os.Stderr, err)
				status = 2
				break
			}
			status = 2
			continue
		}
		res := mf.Format(req)
		if res.Status != 0 {
			status = 2
		}
		if err := enc.Encode(res); err != nil {
			fmt.Fprintln(os.Stderr, err)
			status = 2
			break
		}
	}
	if err := in.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if flushErr := out.Flush(); flushErr != nil {
			fmt.Fprintln(os.Stderr, flushErr)
		}
		os.Exit(2)
	}
	if err := out.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if status != 0 {
		os.Exit(status)
	}
}
