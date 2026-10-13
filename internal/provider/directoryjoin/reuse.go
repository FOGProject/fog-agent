package directoryjoin

// nerrUserExists is NERR_UserExists, "The account already exists.": a join
// that asked to create the computer object found one there by that name.
const nerrUserExists = 2224

// joinOrReuse runs a join that creates the computer object, and when the
// object is already there, runs it again to take that object over.
//
// The object is already there whenever a machine is reimaged: the old image
// joined it, and nothing removed the object when the disk was wiped. A join
// that only creates fails on every one of those machines. The retry joins
// the existing object instead, so its group memberships, its SID and
// whatever was escrowed to it carry over to the new image. The legacy client
// made the same retry on the same code.
//
// join is one NetJoinDomain call; create says whether it may create the
// object. Kept apart from the syscall so the decision is testable on every
// platform.
func joinOrReuse(join func(create bool) uint32) uint32 {
	status := join(true)
	if status == nerrUserExists {
		status = join(false)
	}
	return status
}
