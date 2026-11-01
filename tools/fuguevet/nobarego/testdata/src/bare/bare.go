package bare

func Spawn() {
	go func() {}() // want `bare go statement`
}

func SpawnNamed() {
	go work() // want `bare go statement`
}

func work() {}
