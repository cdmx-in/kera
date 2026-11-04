package app

// mounts lists feature packages in registration order. Each entry is
// {"name", pkg.Mount}; a feature registers its routes on a.Server.
var mounts = []mount{}
