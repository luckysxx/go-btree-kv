package main

import (
	"fmt"
	"os"
)

func main() {
	fp, err := os.OpenFile("/tmp/hello.txt",
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		panic(err)
	}
	defer fp.Close()

	fmt.Printf("打开成功，fd = %d\n", fp.Fd())
	fmt.Printf("文件路径 = %s\n", fp.Name())
}
