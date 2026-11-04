package app

// mounts lists feature packages in registration order. A feature calls
// Register from its init(); cmd/kera imports the feature package (blank
// import when it has no CLI), which keeps app free of feature imports.
var mounts []mount

// Register adds a feature's Mount function. Call it from init().
func Register(name string, fn func(*App) error) { mounts = append(mounts, mount{name: name, fn: fn}) }
