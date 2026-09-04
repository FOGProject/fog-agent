package printerset

// Native is the print subsystem of this platform.
func Native() Backend { return Winspool{} }
