package main

import "path/filepath"

// metalFile is the opt-in marker for the experimental Metal build on an Intel Mac without AVX2
// (for example a 2013 Mac Pro with AMD FirePro GPUs). Create an empty file with this name in the
// node's data folder to try it; Outpost deletes it again if the Metal build keeps failing.
func (n *Node) metalFile() string { return filepath.Join(n.dataDir, "metal") }
