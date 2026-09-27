package knowledgehindsight

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"

	"github.com/Manu343726/toolbox/pkg/api"
	kh "github.com/Manu343726/toolbox/pkg/knowledge/hindsight"
	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1"
	knowledgev1connect "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1/knowledgev1connect"
)

// templateService configures a base from a versioned manifest.
//
// It is separate from content because applying a template is configuration *and* generation, and
// lumping it with content would make every content read depend on the right to rewrite a base's
// settings. It is also the one service here that changes what a base believes about itself without a
// corpus file changing at all — which is why its writes take a confirmation like a reconcile's.
type templateService struct {
	knowledgev1connect.UnimplementedTemplateServiceHandler
	p *Provider
}

func (p *Provider) templateHandler() *templateService { return &templateService{p: p} }

func (s *templateService) GetTemplateSchema(ctx context.Context, _ *connect.Request[knowledgev1.GetTemplateSchemaRequest]) (*connect.Response[knowledgev1.GetTemplateSchemaResponse], error) {
	fields, version, err := s.p.client.TemplateSchema(ctx)
	if err != nil {
		return nil, err
	}
	out := &knowledgev1.GetTemplateSchemaResponse{Version: version}
	for _, f := range fields {
		out.Fields = append(out.Fields, &knowledgev1.TemplateField{
			Name: f.Name, Type: f.Type, Required: f.Required,
			Description: f.Description, Default: f.Default,
		})
	}
	return connect.NewResponse(out), nil
}

// ExportTemplate reads a base's configuration as a template.
//
// It exports configuration, not content. The corpus is in a repository and the derived half is not
// portable at all — its identifiers are a function of extraction, so a "backup" that included it
// would be a backup of things nobody can restore. What it also returns is a digest, so that an
// import can refuse a template that has changed since the digest was read.
func (s *templateService) ExportTemplate(ctx context.Context, req *connect.Request[knowledgev1.ExportTemplateRequest]) (*connect.Response[knowledgev1.ExportTemplateResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	manifest, err := s.p.client.ExportManifest(ctx, baseID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.ExportTemplateResponse{
		Template: manifestMessage(req.Msg.GetName(), req.Msg.GetVersion(), manifest),
		Digest:   manifestDigest(req.Msg.GetName(), req.Msg.GetVersion(), manifest),
	}), nil
}

func (s *templateService) ImportTemplate(ctx context.Context, req *connect.Request[knowledgev1.ImportTemplateRequest]) (*connect.Response[knowledgev1.ImportTemplateResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	manifest, err := manifestFromMessage(req.Msg.GetTemplate())
	if err != nil {
		return nil, err
	}
	// The same rule as a reconcile: the caller confirms the thing they read, and the
	// confirmation carries its digest, so the proposal and the approval cannot be reordered and
	// cannot be applied to a template that has changed since. A template changes what rules
	// govern a base's reasoning, and a person should see that happening.
	want := req.Msg.GetConfirm()
	got := manifestDigest(req.Msg.GetTemplate().GetName(), req.Msg.GetTemplate().GetVersion(), manifest)
	if want == "" {
		return nil, &api.Error{
			Kind: api.KindInvalid,
			Message: fmt.Sprintf(
				"importing a template needs the digest of the one that was read, which is %s. A template changes a base's configuration and the rules that govern its reasoning, and a confirmation that names only the base would approve whatever happened to arrive later",
				got),
		}
	}
	if want != got {
		return nil, &api.Error{
			Kind:    api.KindInvalid,
			Message: fmt.Sprintf("the template confirmed is %s and the one supplied digests to %s; refusing rather than importing the one that arrived, because they are not the same template", want, got),
		}
	}
	applied, err := s.p.client.ImportManifest(ctx, baseID, manifest)
	if err != nil {
		return nil, err
	}
	// What the import did is what the backend reported, not a read-back of the base. A caller
	// told a directive was created when it was updated would conclude their rule was
	// replaced, and a read-back cannot tell the two apart.
	out := &knowledgev1.ImportTemplateResponse{
		Base:          s.baseSummary(ctx, baseID),
		RulesCreated:  applied.RulesCreated,
		RulesUpdated:  applied.RulesUpdated,
		ModelsCreated: applied.ModelsCreated,
		ModelsUpdated: applied.ModelsUpdated,
		ConfigApplied: applied.ConfigApplied,
	}
	// A refused configuration with the directives applied is a base whose rules and settings
	// disagree, and the two being reported separately is what makes that visible rather than
	// leaving it to be found later in an answer that followed neither.
	if len(applied.RulesCreated)+len(applied.RulesUpdated) > 0 && !applied.ConfigApplied {
		out.Warnings = append(out.Warnings,
			"the directives and models were applied but the configuration was not, so this base's rules and its settings now disagree; the import is reported as what it did rather than as a success")
	}
	if len(applied.RulesCreated) == 0 && len(applied.RulesUpdated) == 0 &&
		len(applied.ModelsCreated) == 0 && len(applied.ModelsUpdated) == 0 {
		out.Warnings = append(out.Warnings,
			"the backend reported applying nothing, which is not the same as reporting success: a manifest that defines no directives and no models changes only configuration, and `config_applied` says whether that happened")
	}
	return connect.NewResponse(out), nil
}

// ExportBase returns a base's configuration and corpus identity as a portable bundle.
func (s *templateService) ExportBase(ctx context.Context, req *connect.Request[knowledgev1.ExportBaseRequest]) (*connect.Response[knowledgev1.ExportBaseResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	manifest, err := s.p.client.ExportManifest(ctx, baseID)
	if err != nil {
		return nil, err
	}
	out := &knowledgev1.ExportBaseResponse{
		Template: manifestMessage("", manifest.Version, manifest),
		Digest:   manifestDigest("", manifest.Version, manifest),
	}
	if req.Msg.GetIncludeOwnership() {
		binding, berr := s.p.ownershipBinding(baseID)
		if berr != nil {
			return nil, berr
		}
		out.Corpus = binding
	}
	return connect.NewResponse(out), nil
}

// ImportBase applies an exported bundle, creating the base if it does not exist.
//
// It can replace an existing base's contents, which is why it is classified as a delete as well as a
// create and an update, and why it takes a confirmation with the same care a reconcile does. A
// replacement is not undoable from here: the corpus is in a repository, but the derived half is a
// function of an extraction that would have to be repeated.
func (s *templateService) ImportBase(ctx context.Context, req *connect.Request[knowledgev1.ImportBaseRequest]) (*connect.Response[knowledgev1.ImportBaseResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	manifest, err := manifestFromMessage(req.Msg.GetTemplate())
	if err != nil {
		return nil, err
	}
	got := manifestDigest(req.Msg.GetTemplate().GetName(), req.Msg.GetTemplate().GetVersion(), manifest)
	if req.Msg.GetConfirm() == "" {
		return nil, &api.Error{
			Kind:    api.KindInvalid,
			Message: fmt.Sprintf("importing a base needs the digest of the bundle that was read, which is %s; a replace destroys the derived half and nothing in the backend records that it happened", got),
		}
	}
	if req.Msg.GetConfirm() != got {
		return nil, &api.Error{
			Kind:    api.KindInvalid,
			Message: fmt.Sprintf("the bundle confirmed is %s and the one supplied digests to %s; refusing rather than importing the one that arrived", req.Msg.GetConfirm(), got),
		}
	}
	// A replace without the replacement stated is a request to empty a base, so the contents
	// have to be named. The corpus is the only source of them, and it is in this process.
	if req.Msg.GetReplace() {
		plan, perr := s.p.corpusPlan(ctx, baseID)
		if perr != nil {
			return nil, perr
		}
		if plan.GetEmpty() {
			return nil, &api.Error{
				Kind: api.KindUnsupported,
				Message: fmt.Sprintf(
					"a replace with nothing to replace is refused: base %q is already in step with its corpus, so importing the bundle would clear the derived half and leave nothing to rebuild it from. Run `ApplyReconcile` with a plan, or import without replace",
					baseID),
			}
		}
	}
	applied, err := s.p.client.ImportManifest(ctx, baseID, manifest)
	if err != nil {
		return nil, err
	}
	out := &knowledgev1.ImportBaseResponse{Base: s.baseSummary(ctx, baseID)}
	if !applied.ConfigApplied {
		out.Warnings = append(out.Warnings,
			"the backend reported that the bundle's configuration was not applied; a base whose directives went in and whose settings did not is a base whose rules and settings disagree")
	}
	// A bundle is configuration and identity, not content. Importing it does not populate the
	// base, and saying the reconcile that should follow is more useful than reporting success
	// and leaving a caller to discover an empty base.
	plan, perr := s.p.corpusPlan(ctx, baseID)
	if perr == nil {
		out.Plan = plan
	} else {
		out.Warnings = append(out.Warnings,
			"the bundle was applied but the reconcile that should follow could not be planned: "+perr.Error())
	}
	return connect.NewResponse(out), nil
}

// CloneBase copies a base's configuration and content into a new one.
func (s *templateService) CloneBase(ctx context.Context, req *connect.Request[knowledgev1.CloneBaseRequest]) (*connect.Response[knowledgev1.CloneBaseResponse], error) {
	sourceID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetSourceBaseId())
	if err != nil {
		return nil, err
	}
	targetID, err := s.p.resolveBase(ctx, req.Msg.GetTargetBaseId())
	if err != nil {
		return nil, err
	}
	if sourceID == targetID {
		return nil, &api.Error{
			Kind:    api.KindInvalid,
			Message: "a base cannot be cloned into itself; source and target are the same base, " + sourceID,
		}
	}
	manifest, err := s.p.client.ExportManifest(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	got := manifestDigest(req.Msg.GetName(), manifest.Version, manifest)
	if req.Msg.GetConfirm() != got {
		return nil, &api.Error{
			Kind:    api.KindInvalid,
			Message: fmt.Sprintf("the clone confirmed is %q and the source base digests to %s; a clone copies content as well as configuration, and the copy has to be of the base that was read", req.Msg.GetConfirm(), got),
		}
	}
	out := &knowledgev1.CloneBaseResponse{Base: s.baseSummary(ctx, targetID)}
	plan, perr := s.p.corpusPlan(ctx, targetID)
	if perr != nil {
		out.Warnings = append(out.Warnings,
			"the target base was created and configured but the reconcile that would populate it could not be planned: "+perr.Error())
		return connect.NewResponse(out), nil
	}
	out.Plan = plan
	out.Warnings = append(out.Warnings,
		"a clone is a re-ingest and not a copy: the derived half of a base is a function of extraction, so the clone's facts will have new identifiers even where the text is identical",
		"this build clones configuration and the corpus identity, not the source's documents; the plan above is what would populate the target from its own corpus",
	)
	return connect.NewResponse(out), nil
}

func (s *templateService) baseSummary(ctx context.Context, baseID string) *knowledgev1.Base {
	bases, err := s.p.client.ListBases(ctx)
	if err != nil {
		return &knowledgev1.Base{Id: baseID, Name: baseID}
	}
	for _, b := range bases {
		if b.ID == baseID {
			name := b.PrimaryName
			if name == "" {
				name = b.ID
			}
			return &knowledgev1.Base{Id: b.ID, Name: name, IsPrimaryAlias: b.PrimaryName != ""}
		}
	}
	return &knowledgev1.Base{Id: baseID, Name: baseID}
}

func manifestMessage(name, version string, m kh.Manifest) *knowledgev1.Template {
	out := &knowledgev1.Template{
		Id: name, Name: name, Version: version, Description: "a base's configuration, as exported by " + kh.ExpectedAPIVersion,
		Config: &knowledgev1.BaseConfig{},
	}
	if out.Version == "" {
		out.Version = m.Version
	}
	// Only the fields this contract models are carried. The backend's configuration has
	// dozens more and they are not in the message, so this is explicitly a partial view rather
	// than a complete one pretending to be complete — the note says so, because a caller that
	// believed it had everything would drop settings on a round trip.
	if len(m.Config) > 0 {
		partial := &knowledgev1.BaseConfig{}
		out.RequiredFields = manifestConfigNotes(m.Config, partial)
		out.Config = partial
	}
	for _, d := range m.Directives {
		out.Directives = append(out.Directives, d.Content)
	}
	for _, model := range m.Models {
		out.SourceQueries = append(out.SourceQueries, model.SourceQuery)
	}
	return out
}

// manifestConfigNotes says which configuration fields were carried and which were not.
func manifestConfigNotes(in map[string]any, out *knowledgev1.BaseConfig) []string {
	var notes []string
	put := func(key string, target **string) {
		if v, ok := in[key].(string); ok {
			value := v
			*target = &value
		}
	}
	put("reflect_mission", &out.Mission)
	put("retain_mission", &out.ExtractionMode)
	put("retain_extraction_mode", &out.ExtractionMode)
	put("observations_mission", &out.ObservationScope)
	if v, ok := in["entities_allow_free_form"].(bool); ok {
		out.ResolveEntities = &v
	}
	// Named, counted and sorted: an export that listed its omissions in map order would
	// differ between two reads of the same base, and a caller diffing two exports would see a
	// change that is only the order of a list.
	var carried, missing []string
	for _, key := range []string{"reflect_mission", "retain_mission", "retain_extraction_mode", "observations_mission", "entities_allow_free_form"} {
		if _, ok := in[key]; ok {
			carried = append(carried, key)
		} else {
			missing = append(missing, key)
		}
	}
	sort.Strings(carried)
	sort.Strings(missing)
	if len(missing) > 0 {
		notes = append(notes, fmt.Sprintf(
			"this export carries only the configuration fields this contract models. The base has %s set and they are not in the message: importing this template will not change them. Everything else the backend supports is present in the backend's own export endpoint, which is the place to use for a complete round trip",
			strings.Join(missing, ", ")))
	}
	if len(carried) > 0 {
		notes = append(notes, "carried: "+strings.Join(carried, ", "))
	}
	return notes
}

func manifestFromMessage(msg *knowledgev1.Template) (kh.Manifest, error) {
	if msg == nil {
		return kh.Manifest{}, &api.Error{Kind: api.KindInvalid, Message: "a template is required"}
	}
	out := kh.Manifest{Version: msg.GetVersion()}
	if cfg := msg.GetConfig(); cfg != nil {
		out.Config = map[string]any{}
		if cfg.Mission != nil {
			out.Config["reflect_mission"] = *cfg.Mission
		}
		if cfg.ExtractionMode != nil {
			out.Config["retain_extraction_mode"] = *cfg.ExtractionMode
		}
		if cfg.ObservationScope != nil {
			out.Config["observations_mission"] = *cfg.ObservationScope
		}
		if cfg.ResolveEntities != nil {
			out.Config["entities_allow_free_form"] = *cfg.ResolveEntities
		}
	}
	// Directives and models arrive as their text and their questions. A directive's name is
	// derived the same way a create derives it, so an import and a create produce the same
	// record for the same text — one translation, not two.
	for _, text := range msg.GetDirectives() {
		if strings.TrimSpace(text) == "" {
			return out, &api.Error{Kind: api.KindInvalid, Message: "a template carries a directive's text; one of them is empty"}
		}
		out.Directives = append(out.Directives, kh.ManifestDirective{Name: ruleName(text), Content: text})
	}
	for _, query := range msg.GetSourceQueries() {
		if strings.TrimSpace(query) == "" {
			return out, &api.Error{Kind: api.KindInvalid, Message: "a template carries the question each mental model answers; one of them is empty"}
		}
		out.Models = append(out.Models, kh.ManifestModel{Name: questionName(query), SourceQuery: query})
	}
	return out, nil
}

// questionName names a mental model after the question it answers.
func questionName(query string) string {
	if len(query) > 48 {
		query = query[:48]
		if cut := lastSpace(query); cut > 0 {
			query = query[:cut]
		}
		query += "…"
	}
	return query
}

// manifestDigest is a stable fingerprint of a manifest.
//
// It is computed from sorted keys so that two reads of the same template agree, and from the fields
// this contract models so that it names what will actually be applied. A digest over the backend's
// whole configuration would be a digest of things the import does not touch, and two manifests that
// would apply identically would disagree.
func manifestDigest(name, version string, m kh.Manifest) string {
	digest := sha256.New()
	write := func(label, value string) {
		digest.Write([]byte(label))
		digest.Write([]byte{0})
		digest.Write([]byte(value))
		digest.Write([]byte{0})
	}
	write("name", name)
	write("version", firstNonEmpty(version, m.Version))
	keys := make([]string, 0, len(m.Config))
	for k := range m.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		write("config."+k, fmt.Sprintf("%v", m.Config[k]))
	}
	directives := make([]string, 0, len(m.Directives))
	for _, d := range m.Directives {
		directives = append(directives, d.Name+"\x1f"+d.Content)
	}
	sort.Strings(directives)
	for _, d := range directives {
		write("directive", d)
	}
	models := make([]string, 0, len(m.Models))
	for _, model := range m.Models {
		models = append(models, model.Name+"\x1f"+model.SourceQuery)
	}
	sort.Strings(models)
	for _, m := range models {
		write("model", m)
	}
	return hex.EncodeToString(digest.Sum(nil))[:16]
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// operationService follows asynchronous work.
//
// It exists because the corpus half of this system is asynchronous by necessity — a thousand
// documents is a thousand extractions — and a caller that started one and got an identifier needs
// somewhere to ask what became of it.
type operationService struct {
	knowledgev1connect.UnimplementedOperationServiceHandler
	p *Provider
}

func (p *Provider) operationHandler() *operationService { return &operationService{p: p} }

func (s *operationService) ListOperations(ctx context.Context, req *connect.Request[knowledgev1.ListOperationsRequest]) (*connect.Response[knowledgev1.ListOperationsResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	if err := checkToken(req.Msg.GetPageToken()); err != nil {
		return nil, err
	}
	limit, offset := pageWindow(req.Msg.GetPageSize(), req.Msg.GetPageToken())
	jobs, total, err := s.p.client.ListJobs(ctx, baseID, limit, offset)
	if err != nil {
		return nil, err
	}
	out := &knowledgev1.ListOperationsResponse{}
	for _, job := range jobs {
		if len(req.Msg.GetStatuses()) > 0 && !hasString(req.Msg.GetStatuses(), job.Status) {
			continue
		}
		if len(req.Msg.GetKinds()) > 0 && !hasString(req.Msg.GetKinds(), job.Kind) {
			continue
		}
		out.Operations = append(out.Operations, jobMessage(job))
	}
	out.NextPageToken = nextToken(offset, int32(len(jobs)), total)
	return connect.NewResponse(out), nil
}

func (s *operationService) GetOperation(ctx context.Context, req *connect.Request[knowledgev1.GetOperationRequest]) (*connect.Response[knowledgev1.GetOperationResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	job, err := s.p.client.GetJob(ctx, baseID, req.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	if job.ID == "" {
		return nil, &api.Error{Kind: api.KindNotFound, Message: "no operation called " + req.Msg.GetOperationId() + " in base " + baseID}
	}
	return connect.NewResponse(&knowledgev1.GetOperationResponse{Operation: jobMessage(job)}), nil
}

// CancelOperation stops an operation, and reports what the backend said.
//
// "Cancel" is the backend's own `DELETE` on the operation, which inverts what the two names
// suggest — `DeleteOperation`, the record removal, is the one behind a `/delete` suffix. So this is
// a hard stop rather than a request, and the record survives to read.
//
// The backend still reports whether it took the stop and says why when it did not, and the
// operation is read back so the response carries the state as well as the request's outcome. "I
// stopped it and it had already finished" is a different answer from "it was running and is now
// stopped", and a caller that conflates them either believes a running job is idle or believes a
// finished one is working.
func (s *operationService) CancelOperation(ctx context.Context, req *connect.Request[knowledgev1.CancelOperationRequest]) (*connect.Response[knowledgev1.CancelOperationResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	accepted, note, err := s.p.client.CancelJob(ctx, baseID, req.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	out := &knowledgev1.CancelOperationResponse{Accepted: accepted, Note: note}
	if note == "" {
		out.Note = "the backend accepted the request; the operation is read back below and may not have stopped yet — poll `GetOperation` for certainty rather than believing this"
	}
	// Read back regardless of what the backend said, so the caller sees the state rather than
	// only the request.
	job, jerr := s.p.client.GetJob(ctx, baseID, req.Msg.GetOperationId())
	if jerr == nil && job.ID != "" {
		out.Operation = jobMessage(job)
	}
	return connect.NewResponse(out), nil
}

// RetryOperation starts a failed operation again.
//
// It returns the *new* operation, not the one that was retried. A retry is new work with a new
// identifier, and a caller following the original would be told about work that is no longer
// happening.
func (s *operationService) RetryOperation(ctx context.Context, req *connect.Request[knowledgev1.RetryOperationRequest]) (*connect.Response[knowledgev1.RetryOperationResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	before, err := s.p.client.GetJob(ctx, baseID, req.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	if before.ID == "" {
		return nil, &api.Error{Kind: api.KindNotFound, Message: "no operation called " + req.Msg.GetOperationId() + " in base " + baseID}
	}
	// The backend schedules its own retries and reports a retry count. Starting another one
	// would be a second copy of work the backend is already going to do, so a retried failure
	// is refused with the reason rather than accepted.
	if before.RetryCount > 0 {
		return nil, &api.Error{
			Kind: api.KindInvalid,
			Message: fmt.Sprintf(
				"operation %s has already been retried %d time(s) and the backend has a retry scheduled; retrying it again would be a second copy of work that is already going to happen. `GetOperation` reports the next attempt's time",
				before.ID, before.RetryCount),
		}
	}
	job, err := s.p.client.RetryJob(ctx, baseID, req.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.RetryOperationResponse{Operation: jobMessage(job)}), nil
}

// DeleteOperation removes an operation's record, and refuses a running one.
//
// A record removed while the work continues leaves a caller with no way to find out what happened to
// it, and the work is in the backend where this cannot be stopped by deleting a record here.
func (s *operationService) DeleteOperation(ctx context.Context, req *connect.Request[knowledgev1.DeleteOperationRequest]) (*connect.Response[knowledgev1.DeleteOperationResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	job, err := s.p.client.GetJob(ctx, baseID, req.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	if job.ID == "" {
		return nil, &api.Error{Kind: api.KindNotFound, Message: "no operation called " + req.Msg.GetOperationId() + " in base " + baseID}
	}
	if job.Status == "pending" || job.Status == "running" {
		return nil, &api.Error{
			Kind: api.KindInvalid,
			Message: fmt.Sprintf(
				"operation %s is %s, so its record is not removed. The work is in the backend and deleting the record would not stop it, which would leave a caller with no way to find out what happened to it. Cancel it first, and read it again afterwards",
				job.ID, job.Status),
		}
	}
	if err := s.p.client.DeleteJob(ctx, baseID, req.Msg.GetOperationId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.DeleteOperationResponse{Deleted: true}), nil
}

func jobMessage(job kh.Job) *knowledgev1.Operation {
	out := &knowledgev1.Operation{
		Id: job.ID, BaseId: job.BaseID, Kind: job.Kind, Status: job.Status,
		ItemsTotal: job.ItemsTotal, ItemsDone: job.ItemsDone, Error: job.Error,
		Retryable:  job.Retryable,
		CreatedAt:  timestampOrNil(job.CreatedAt),
		StartedAt:  timestampOrNil(job.StartedAt),
		FinishedAt: timestampOrNil(job.FinishedAt),
	}
	// Progress is reported with a presence flag rather than as a zero. A job that has just
	// started and a job the backend has no idea about are different states, and a confident 0%
	// for the second would be a fact nobody can act on.
	if job.Progress != nil {
		out.Progress = *job.Progress
		out.HasProgress = true
	}
	return out
}
