package skillgit

import (
	"context"
	"os"
	"strings"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	skillgitv1 "github.com/Manu343726/toolbox/subsystems/skillgit/skillgitv1"
)

// The management service: which sources exist, and what is done to them.
//
// It is separate from the catalog contract because a catalog contract is about *skills* and
// these operations are about where skills come from. A catalog provider serves skills; deciding
// what to clone is not a catalog's business, and a contract that mixed the two would make every
// provider responsible for it.

// ListCheckouts returns the state of every checkout this provider holds.
//
// Which catalogs are *served* is the catalog contract's question, and it is answered there
// by every provider. This one is about this provider's own storage: which commit, whether it
// is clean, whether it can sync. Those are facts about a directory, and they are the ones a
// person deciding whether to depend on a checkout needs and a caller of the catalog contract
// has no way to ask for.
func (s *Service) ListCheckouts(
	ctx context.Context, _ *connect.Request[skillgitv1.ListCheckoutsRequest],
) (*connect.Response[skillgitv1.ListCheckoutsResponse], error) {
	statuses, err := s.sortedStatuses(ctx)
	if err != nil {
		return nil, connectFailure(err)
	}
	return connect.NewResponse(&skillgitv1.ListCheckoutsResponse{Checkouts: statuses}), nil
}

// RegisterCatalog clones a remote and serves it as a catalog under a name.
//
// The clone is shallow, because a catalog serves a repository's current content and a full
// history of every repository a deployment has ever been asked about is disk nobody asked for.
// The name is refused if taken, so two checkouts of one repository can both be served under
// different names — which is occasionally what somebody wants — while one name never silently
// means two catalogs.
func (s *Service) RegisterCatalog(
	ctx context.Context, request *connect.Request[skillgitv1.RegisterCatalogRequest],
) (*connect.Response[skillgitv1.RegisterCatalogResponse], error) {
	id := strings.TrimSpace(request.Msg.GetId())
	named := strings.TrimSpace(request.Msg.GetRemote())
	if named == "" {
		return nil, connectFailure(api.Errorf(api.KindInvalid,
			"a catalog needs a remote. It may be a URL, an scp-style remote, a local path, "+
				"or the %s shorthand for a repository on GitHub", "owner/repo"))
	}
	// The name is validated before anything touches the filesystem, because a name that
	// could escape this provider's own directory must never become a path — and because a
	// refusal that arrived after a minute-long clone would be a poor trade.
	if err := validateCatalogID(id); err != nil {
		return nil, connectFailure(err)
	}
	remote := RemoteURL(named)
	registration := Registration{
		ID: id,
		// Recorded without the credential, because this file is readable, is often
		// committed, and is frequently pasted into a bug report. What is kept is the fact
		// that a credential was involved, because a remote with one and a remote without one
		// are different things to depend on.
		Remote:    CredentialFreeRemote(remote),
		Auth:      authFor(remote),
		Directory: id,
		CreatedAt: stamp(s.now),
		// A catalog registered from a remote is one this deployment found, and a deployment
		// that found a repository will not commit to it: that is a decision about somebody
		// else's repository, and pushing is a separate, explicit act instead.
		ReadOnly: true,
	}
	// Checked before the clone, so a taken name costs nothing and says so immediately.
	if _, taken := s.registry.Get(id); taken {
		return nil, connectFailure(api.Errorf(api.KindAlreadyExists,
			"a catalog is already registered as %q. A name means one catalog, so register this "+
				"one under another name — two checkouts of one repository are both allowed, and "+
				"sharing one name is not", id))
	}
	if err := s.git.Clone(ctx, remote, s.registry.DirectoryFor(id)); err != nil {
		return nil, connectFailure(err)
	}
	if err := s.registry.Add(registration); err != nil {
		// The checkout is left where it is rather than removed on a failure to record it: a
		// clone of somebody's repository that took a minute to fetch is not something to throw
		// away because a name turned out to be awkward, and the directory is where a person
		// would look.
		return nil, connectFailure(err)
	}
	// The record is written here, because a registration that exists only in this process is
	// not a registration: a deployment that forgot its catalogs on reboot would serve a
	// different set of skills each time it came up, and a project's `skills:` list would then
	// name references that resolve to nothing.
	if err := s.registry.Save(); err != nil {
		return nil, connectFailure(err)
	}
	s.forget(id)
	return s.registeredResponse(ctx, registration)
}

// CreateCatalog makes a new repository and serves it, pushing only if a remote was given.
//
// Creating a catalog is local on purpose: a new repository is made where the deployment runs
// and served from there, and it is pushed only when a remote is named. No forge is contacted
// and no token is needed to start a catalog — which means a deployment can offer git-backed
// catalogs on a machine with no account anywhere, and publishing one is a separate, deliberate
// act.
func (s *Service) CreateCatalog(
	ctx context.Context, request *connect.Request[skillgitv1.CreateCatalogRequest],
) (*connect.Response[skillgitv1.CreateCatalogResponse], error) {
	id := strings.TrimSpace(request.Msg.GetId())
	if err := validateCatalogID(id); err != nil {
		return nil, connectFailure(err)
	}
	if _, taken := s.registry.Get(id); taken {
		return nil, connectFailure(api.Errorf(api.KindAlreadyExists,
			"a catalog is already registered as %q, so it cannot be created under that name", id))
	}
	directory := s.registry.DirectoryFor(id)
	if err := s.git.Init(ctx, directory, s.identity); err != nil {
		return nil, connectFailure(err)
	}
	if err := writeSeed(directory, request.Msg.GetSeed()); err != nil {
		return nil, connectFailure(err)
	}
	// The seed is the first commit, so a catalog created with a skill in it has that skill in
	// its history rather than appearing in a later commit as though it had always been there.
	if err := s.git.Commit(ctx, directory, "The first skills of a new catalog.", s.identity); err != nil {
		return nil, connectFailure(err)
	}
	registration := Registration{
		ID:        id,
		Directory: id,
		CreatedAt: stamp(s.now),
		// A catalog this deployment created is one it owns, so it may be written to. That is
		// the distinction from a registered one, and it is the whole difference.
		ReadOnly: false,
	}
	if named := strings.TrimSpace(request.Msg.GetRemote()); named != "" {
		remote := RemoteURL(named)
		if err := s.git.Remote(ctx, directory, remote); err != nil {
			return nil, connectFailure(err)
		}
		// git holds the remote as given, in the checkout's own config; the registration
		// holds it without a credential, because the registration is a file a person reads.
		registration.Remote = CredentialFreeRemote(remote)
		registration.Auth = authFor(remote)
	}
	if err := s.registry.Add(registration); err != nil {
		return nil, connectFailure(err)
	}
	if err := s.registry.Save(); err != nil {
		return nil, connectFailure(err)
	}
	s.forget(id)
	status, err := s.status(ctx, registration)
	if err != nil {
		return nil, connectFailure(err)
	}
	return connect.NewResponse(&skillgitv1.CreateCatalogResponse{Checkout: status}), nil
}

// SyncCatalog updates a catalog from its remote.
//
// A sync that cannot be completed is reported with the repository's path and **nothing is
// resolved**. A catalog that resolved its own conflicts would be choosing a side in somebody
// else's repository, and the person who can decide what a conflict meant is the person who owns
// it — so the error names where to look and stops.
func (s *Service) SyncCatalog(
	ctx context.Context, request *connect.Request[skillgitv1.SyncCatalogRequest],
) (*connect.Response[skillgitv1.SyncCatalogResponse], error) {
	registration, err := s.registration(request.Msg.GetId())
	if err != nil {
		return nil, connectFailure(err)
	}
	directory := s.checkout(registration)
	before, _ := s.git.Head(ctx, directory)
	if err := s.git.Pull(ctx, directory); err != nil {
		return nil, connectFailure(err)
	}
	after, _ := s.git.Head(ctx, directory)
	// The cached reader is dropped, so a sync that brought in new content is read rather than
	// served from what the checkout held before it.
	s.forget(registration.ID)
	status, err := s.status(ctx, registration)
	if err != nil {
		return nil, connectFailure(err)
	}
	return connect.NewResponse(&skillgitv1.SyncCatalogResponse{
		Checkout:     status,
		PreviousHead: before,
		Head:         after,
		Changed:      before != after,
	}), nil
}

// PushCatalog sends a catalog's commits to its remote.
//
// An optional message commits whatever is uncommitted first, so a caller can say what a change
// was. Without one, an uncommitted checkout is refused rather than pushed: a modification is one
// commit, and pushing a change that was never recorded is a commit somebody else will have to
// reconstruct.
func (s *Service) PushCatalog(
	ctx context.Context, request *connect.Request[skillgitv1.PushCatalogRequest],
) (*connect.Response[skillgitv1.PushCatalogResponse], error) {
	registration, err := s.registration(request.Msg.GetId())
	if err != nil {
		return nil, connectFailure(err)
	}
	directory := s.checkout(registration)
	if message := strings.TrimSpace(request.Msg.GetMessage()); message != "" {
		if err := s.git.Commit(ctx, directory, message, s.identity); err != nil {
			return nil, connectFailure(err)
		}
	}
	clean, err := s.git.Clean(ctx, directory)
	if err != nil {
		return nil, connectFailure(err)
	}
	if !clean {
		pending, _ := s.git.run(ctx, directory, "status", "--porcelain")
		return nil, connectFailure(api.Errorf(api.KindFailedPrecondition,
			"the catalog at %s has changes that are not committed:\n%s\nA modification is one "+
				"commit, so pass a message describing it", directory, pending))
	}
	if err := s.git.Push(ctx, directory); err != nil {
		return nil, connectFailure(err)
	}
	head, _ := s.git.Head(ctx, directory)
	status, err := s.status(ctx, registration)
	if err != nil {
		return nil, connectFailure(err)
	}
	return connect.NewResponse(&skillgitv1.PushCatalogResponse{
		Checkout: status, Head: head, Pushed: true,
	}), nil
}

// UnregisterCatalog stops serving a catalog and leaves its checkout where it is.
//
// The skills are still readable on disk and nothing is destroyed. That separation is the point:
// the destructive operation is a separate, separately named call, so nothing is removed without
// being asked for twice.
func (s *Service) UnregisterCatalog(
	_ context.Context, request *connect.Request[skillgitv1.UnregisterCatalogRequest],
) (*connect.Response[skillgitv1.UnregisterCatalogResponse], error) {
	registration, found := s.registry.Forget(request.Msg.GetId())
	if !found {
		return nil, connectFailure(api.Errorf(api.KindNotFound,
			"no catalog is registered as %q, so there is nothing to stop serving", request.Msg.GetId()))
	}
	if err := s.registry.Save(); err != nil {
		return nil, connectFailure(err)
	}
	s.forget(registration.ID)
	return connect.NewResponse(&skillgitv1.UnregisterCatalogResponse{
		Id:       registration.ID,
		Location: s.checkout(registration),
	}), nil
}

// DeleteCatalog unregisters a catalog **and** removes its checkout.
//
// It is a separate method from UnregisterCatalog rather than an argument to it, because removing
// a directory of somebody's files is a different consequence from forgetting a remote, and a
// method whose name said only "unregister" would be the wrong name for the destructive one.
func (s *Service) DeleteCatalog(
	_ context.Context, request *connect.Request[skillgitv1.DeleteCatalogRequest],
) (*connect.Response[skillgitv1.DeleteCatalogResponse], error) {
	registration, found := s.registry.Forget(request.Msg.GetId())
	if !found {
		return nil, connectFailure(api.Errorf(api.KindNotFound,
			"no catalog is registered as %q, so there is nothing to remove", request.Msg.GetId()))
	}
	location := s.checkout(registration)
	removed := true
	if _, err := os.Stat(location); err != nil {
		if !os.IsNotExist(err) {
			return nil, connectFailure(api.WrapError(api.KindInternal, err, "reading %s", location))
		}
		// A catalog whose checkout is already gone is the outcome that was asked for, so it
		// is reported as removed rather than as a failure.
		removed = false
	} else if err := removeAll(location); err != nil {
		return nil, connectFailure(err)
	}
	// The record is written after the removal, so a failure to remove leaves the catalog
	// registered and a second attempt can finish the job — the opposite order would leave a
	// deployment serving a catalog whose directory is gone.
	if err := s.registry.Save(); err != nil {
		return nil, connectFailure(err)
	}
	s.forget(registration.ID)
	return connect.NewResponse(&skillgitv1.DeleteCatalogResponse{
		Id: registration.ID, Location: location, Removed: removed,
	}), nil
}

// registeredResponse renders the answer to a registration: the catalog, and how many skills it
// turned out to hold.
//
// A repository that publishes its skills under none of the conventions this provider looks in
// is a real outcome — the clone succeeded and there is nothing in it that is a skill — and it is
// reported as a count of zero rather than as a failure, because the catalog is registered and
// will hold whatever is added to it later.
func (s *Service) registeredResponse(
	ctx context.Context, registration Registration,
) (*connect.Response[skillgitv1.RegisterCatalogResponse], error) {
	status, err := s.status(ctx, registration)
	if err != nil {
		return nil, connectFailure(err)
	}
	if status.GetSkills() == 0 {
		status.Note = "A clone of " + describeRemote(registration.Remote) +
			" that currently holds no skills this framework can see. A repository publishes " +
			"skills as a directory with a SKILL.md in it, under one of the conventions its own " +
			"clients read."
	}
	return connect.NewResponse(&skillgitv1.RegisterCatalogResponse{
		Checkout: status, Skills: status.GetSkills(),
	}), nil
}

// forget drops a catalog's cached reader, so a change to its checkout is read rather than
// served from what the checkout held before it.
func (s *Service) forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.directories, id)
}
