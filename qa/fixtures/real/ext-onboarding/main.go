package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println(greeting(argName()))
}

func argName() string {
	if len(os.Args) > 1 {
		return os.Args[1]
	}
	return "world"
}

func greeting(name string) string {
	return "Hello, " + name + "!"
}
