package ir

import "encoding/json"

// Role classifies what a type definition is for. It replaces the v1
// Object/Input TypeKind split with a richer classification; generators key
// off Role instead of inferring from context.
//
// The role set is provisional until the flip: hand-migration PRs may add or
// rename roles as edge cases surface, with a single owner approving changes.
// The enum freezes at the flip.
type Role string

const (
	// RoleDBTable marks a type materialized as a relational table.
	RoleDBTable Role = "DBTable"

	// RoleAPIView marks a type returned by API operations.
	RoleAPIView Role = "APIView"

	// RoleAPIInput marks a type accepted as an API operation input.
	RoleAPIInput Role = "APIInput"

	// RoleEmbeddedStruct marks a type embedded in other types (no table, no route).
	RoleEmbeddedStruct Role = "EmbeddedStruct"

	// RoleAPIOperationSet marks a class declaring a set of API operations.
	RoleAPIOperationSet Role = "APIOperationSet"

	// RoleTrait marks a trait declaration (marker or configurable).
	RoleTrait Role = "Trait"
)

// String returns the string representation of a Role.
func (r Role) String() string {
	return string(r)
}

// TypeKind is retained for legacy runtime schema payloads.
type TypeKind = Role

const (
	// TypeKindObject is retained for legacy runtime schema payloads.
	TypeKindObject TypeKind = RoleAPIView
	// TypeKindInput is retained for legacy runtime schema payloads.
	TypeKindInput TypeKind = RoleAPIInput
)

// TypeDef is a format-agnostic representation of a named type.
//
// In TypeScript: an exported, decorated class declaration.
// In JSON/YAML: an object using these field names verbatim.
type TypeDef struct {
	// Name is the type name.
	Name string `json:"name" yaml:"name"`

	// Owner is the source schema file path this type was defined in.
	Owner string `json:"owner,omitempty" yaml:"owner,omitempty"`

	// Description is the human-readable description.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Comment stores the node-attached comment for the type declaration.
	Comment string `json:"comment,omitempty" yaml:"comment,omitempty"`

	// Role classifies what this type is for. Generators key off Role.
	Role Role `json:"role" yaml:"role"`

	// Kind is retained for legacy runtime schema payloads.
	Kind TypeKind `json:"kind,omitempty" yaml:"kind,omitempty"`

	// Extends is the single base-class name this type inherits fields from.
	// Inheritance is pre-flattened: Fields already includes inherited entries
	// with [FieldDef.InheritedFrom] set. Empty means no base class.
	Extends string `json:"extends,omitempty" yaml:"extends,omitempty"`

	// Implements lists the marker and configurable traits implemented by this type.
	Implements []TraitRef `json:"implements,omitempty" yaml:"implements,omitempty"`

	// RawHeritage records the heritage clauses as written, before flattening.
	// Every reader records this unconditionally: recording is cheap at walk
	// time and unrecoverable later. Flattened Fields remain the canonical
	// surface generators consume; structure-preserving generators (Go
	// interfaces from traits, TS class hierarchies) read RawHeritage.
	RawHeritage *RawHeritage `json:"rawHeritage,omitempty" yaml:"rawHeritage,omitempty"`

	// IsTrait is true if this type is itself a trait declaration.
	IsTrait bool `json:"isTrait,omitempty" yaml:"isTrait,omitempty"`

	// TraitConfig is the configurable-trait Config schema, when the trait is
	// generic. Nil for marker traits and non-trait types.
	TraitConfig *TraitConfigSchema `json:"traitConfig,omitempty" yaml:"traitConfig,omitempty"`

	// Source is the @source(...) cross-layer linkage for verified projections.
	// Nil when the type is not a projection of another service's type.
	Source *SourceRef `json:"source,omitempty" yaml:"source,omitempty"`

	// Fields lists all fields in declaration order, including pre-flattened
	// inherited fields.
	Fields []*FieldDef `json:"fields,omitempty" yaml:"fields,omitempty"`

	// Indexes lists database indexes defined on this type.
	Indexes []IndexDef `json:"indexes,omitempty" yaml:"indexes,omitempty"`

	// JsonField marks this type as a JSON-only payload that should not be
	// materialized as a relational table.
	JsonField bool `json:"jsonField,omitempty" yaml:"jsonField,omitempty"`

	// Versioned marks a DB table for generated history tracking.
	Versioned bool `json:"versioned,omitempty" yaml:"versioned,omitempty"`

	// VersionedConfig carries optional history table lifecycle controls.
	// Nil means @versioned was used with the default append-only behavior.
	VersionedConfig *VersionedConfig `json:"versionedConfig,omitempty" yaml:"versionedConfig,omitempty"`

	// EnvVars indicates this type provides environment variable configuration.
	EnvVars bool `json:"envVars,omitempty" yaml:"envVars,omitempty"`

	// DenyUnknownFields (@denyUnknownFields) makes the generated Rust serde
	// deserializer reject a payload key the type does not declare instead of
	// dropping it. Opt-in per type: a strict decoder suits a closed wire
	// contract and breaks a payload that must survive a newer producer.
	DenyUnknownFields bool `json:"denyUnknownFields,omitempty" yaml:"denyUnknownFields,omitempty"`

	// StrictJSON (@strictJSON) makes every generated decoder of this type,
	// in Go, TypeScript, Python and Rust, reject a key the type does not
	// declare and a required field that is absent or null. It applies to
	// this object only; a nested object type opts in on its own.
	StrictJSON bool `json:"strictJSON,omitempty" yaml:"strictJSON,omitempty"`

	// Projection is the @projection declaration of a RoleProjection type: the
	// view's address, the table it reads and the tables it joins, its row
	// rules and its collapse. Fields are the view's columns. Nil for every
	// other role. See [ProjectionDef].
	Projection *ProjectionDef `json:"projection,omitempty" yaml:"projection,omitempty"`

	// Extensions holds extension decorator data keyed by extension name; see
	// [Schema.Extensions].
	Extensions map[string]json.RawMessage `json:"extensions,omitempty" yaml:"extensions,omitempty"`
}

// VersionedConfig describes optional controls for @versioned history storage.
type VersionedConfig struct {
	// RetentionDays asks generators to emit manual pruning helpers for history
	// rows older than this many days. Nil means no generated retention helper.
	RetentionDays *int `json:"retentionDays,omitempty" yaml:"retentionDays,omitempty"`

	// PartitionBy controls optional history table partitioning. Empty means the
	// default non-partitioned table. "month" means RANGE(recorded_at) partitions.
	PartitionBy string `json:"partitionBy,omitempty" yaml:"partitionBy,omitempty"`

	// PruneKeepReferencedBy excludes history rows whose (key, _version) pair is
	// referenced by another table from the generated prune function, so
	// externally pinned row images survive retention. Empty means the prune
	// function keeps only the latest row per key. Requires RetentionDays.
	//
	// A versioned table can have several independent readers of its historical
	// rows, so this is a list: each entry adds its own exclusion, and declaring
	// a second pin never displaces the first. The TypeScript DSL accepts one
	// reference or an array of them; both land here as a list.
	PruneKeepReferencedBy []*PruneReference `json:"pruneKeepReferencedBy,omitempty" yaml:"pruneKeepReferencedBy,omitempty"`
}

// PruneReference names the table and columns that pin history rows against
// pruning: a history row is kept while a row exists in Table whose KeyColumn
// equals the history row's key and whose VersionColumn equals its _version.
type PruneReference struct {
	// Table is the referencing table name (snake_case, as in DDL).
	Table string `json:"table" yaml:"table"`

	// KeyColumn is the referencing table's column holding the versioned row key.
	KeyColumn string `json:"keyColumn" yaml:"keyColumn"`

	// VersionColumn is the referencing table's column holding the pinned _version.
	VersionColumn string `json:"versionColumn" yaml:"versionColumn"`
}

// TraitRef references a trait implemented by a type, with any resolved
// configuration arguments for configurable traits.
type TraitRef struct {
	// Name is the trait name.
	Name string `json:"name" yaml:"name"`

	// ConfigArgs holds the resolved configuration arguments for configurable
	// traits. Nil for marker traits.
	ConfigArgs map[string]any `json:"configArgs,omitempty" yaml:"configArgs,omitempty"`
}

// TraitConfigSchema describes the Config shape a configurable trait declares
// via its `<Config extends TraitConfig>` generic parameter.
type TraitConfigSchema struct {
	// Fields lists the Config schema fields in declaration order.
	Fields []*FieldDef `json:"fields,omitempty" yaml:"fields,omitempty"`
}

// RawHeritage records the heritage clauses as the author wrote them, before
// flattening. Informational for most generators; canonical input for
// structure-preserving ones.
type RawHeritage struct {
	// Extends is the base-class expression with type arguments, as written.
	Extends string `json:"extends,omitempty" yaml:"extends,omitempty"`

	// Implements lists implements clauses in declaration order, with type arguments.
	Implements []string `json:"implements,omitempty" yaml:"implements,omitempty"`
}

// SourceRef is the @source(...) cross-layer linkage: it names the source type
// this type is a verified projection of.
type SourceRef struct {
	// Target is the service-qualified source type name (e.g., "web-db.User").
	Target string `json:"target" yaml:"target"`

	// Virtual lists field names added by the projection that do not exist on
	// the source type.
	Virtual []string `json:"virtual,omitempty" yaml:"virtual,omitempty"`

	// OmittedFromSource lists field names present on the source type but not
	// on this projection.
	OmittedFromSource []string `json:"omittedFromSource,omitempty" yaml:"omittedFromSource,omitempty"`
}

// FieldDef is a format-agnostic representation of a field within a type or
// operation. It captures all decorator metadata: DB decorators (@key,
// @unique, @relation, ...), API decorators (@auth, @requirePermission, ...),
// and general decorators (@secret, @default, ...).
//
// The field name is the JSON name; custom JSON tags are not a feature of the
// schema language.
type FieldDef struct {
	// Name is the field name as declared in the schema.
	Name string `json:"name" yaml:"name"`

	// JSONTag is retained for legacy runtime schema payloads.
	JSONTag string `json:"jsonTag,omitempty" yaml:"jsonTag,omitempty"`

	// Description is the human-readable field description.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Title is the human-facing label for form rendering (JSON Schema
	// standard `title` key in the legacy form). Empty means derive from Name.
	Title string `json:"title,omitempty" yaml:"title,omitempty"`

	// Placeholder is the input placeholder hint for form rendering
	// (`x-placeholder` in the legacy form). Empty means no hint.
	Placeholder string `json:"placeholder,omitempty" yaml:"placeholder,omitempty"`

	// Comment stores the node-attached comment for the field declaration.
	Comment string `json:"comment,omitempty" yaml:"comment,omitempty"`

	// Docs is the reader-facing documentation of an operation, declared with
	// @docs. Nil on data fields and on operations without @docs.
	Docs *OperationDocs `json:"docs,omitempty" yaml:"docs,omitempty"`

	// TypeRef references the field's type (scalar, enum, object, etc.).
	TypeRef TypeRef `json:"typeRef" yaml:"typeRef"`

	// Required indicates the field is non-nullable.
	// Fields with AutoGenerated=true are typically not required for input validation.
	Required bool `json:"required,omitempty" yaml:"required,omitempty"`

	// InheritedFrom names the base class or trait that contributed this field.
	// Empty for fields declared directly on the type.
	InheritedFrom string `json:"inheritedFrom,omitempty" yaml:"inheritedFrom,omitempty"`

	// Virtual marks a field added by a @source-decorated projection that does
	// not exist in the source type.
	Virtual bool `json:"virtual,omitempty" yaml:"virtual,omitempty"`

	// SourceMustProject marks a source-side (DB) field that @source
	// projections are expected to carry; a projection omitting it draws a
	// verification warning (@sourceMustProject).
	SourceMustProject bool `json:"sourceMustProject,omitempty" yaml:"sourceMustProject,omitempty"`

	// ProjectedFrom is the alias.field a projection column reads (@column).
	// Empty on a projection column means base.<field name>; meaningless on
	// every other role. See [FieldDef.ProjectionSource].
	ProjectedFrom string `json:"projectedFrom,omitempty" yaml:"projectedFrom,omitempty"`

	// ProjectedFunction is the computed form of @column: the column is the
	// result of a named SQL function over alias.field arguments. Exclusive
	// with ProjectedFrom and valid only on projection columns.
	ProjectedFunction *ProjectionFunctionCall `json:"projectedFunction,omitempty" yaml:"projectedFunction,omitempty"`

	// Default is the default value from @default (nil means no default).
	Default *string `json:"default,omitempty" yaml:"default,omitempty"`

	// PlatformDefault names the composite default attached to this field.
	// The name is the target object type and resolves in this schema or a
	// declared dependency.
	PlatformDefault string `json:"platformDefault,omitempty" yaml:"platformDefault,omitempty"`

	// ValidateMin is the minimum numeric value allowed for this field
	// (@validateMin). Nil means no minimum.
	ValidateMin *float64 `json:"validateMin,omitempty" yaml:"validateMin,omitempty"`

	// ValidateMax is the maximum numeric value allowed for this field
	// (@validateMax). Nil means no maximum.
	ValidateMax *float64 `json:"validateMax,omitempty" yaml:"validateMax,omitempty"`

	// ValidateUploadMaxBytes is the largest multipart upload, in bytes, the
	// generated API accepts for this field (Validate<T, { uploadMaxBytes }>).
	// Only valid on a single file-upload scalar field. Nil keeps the
	// scalar's own upload limit.
	ValidateUploadMaxBytes *int64 `json:"validateUploadMaxBytes,omitempty" yaml:"validateUploadMaxBytes,omitempty"`

	// ValidateMinLength is the minimum character length for string-like fields
	// (@validateMinLength). Nil means no minimum.
	ValidateMinLength *int `json:"validateMinLength,omitempty" yaml:"validateMinLength,omitempty"`

	// ValidateMaxLength is the maximum character length for string-like fields
	// (@validateMaxLength). Nil means no maximum.
	ValidateMaxLength *int `json:"validateMaxLength,omitempty" yaml:"validateMaxLength,omitempty"`

	// ValidateListMin is the minimum list cardinality (item count)
	// (@validateListMin). Nil means no minimum.
	ValidateListMin *int `json:"validateListMin,omitempty" yaml:"validateListMin,omitempty"`

	// ValidateListMax is the maximum list cardinality (item count)
	// (@validateListMax). Nil means no maximum.
	ValidateListMax *int `json:"validateListMax,omitempty" yaml:"validateListMax,omitempty"`

	// ValidatePattern is the regex pattern enforced for this field
	// (@validatePattern). Empty string means no pattern.
	ValidatePattern string `json:"validatePattern,omitempty" yaml:"validatePattern,omitempty"`

	// Arguments lists field arguments (primarily used for operation fields).
	Arguments []*ArgumentDef `json:"arguments,omitempty" yaml:"arguments,omitempty"`

	// Key indicates this field is the primary key (@key).
	Key bool `json:"key,omitempty" yaml:"key,omitempty"`

	// Unique indicates this field has a uniqueness constraint (@unique).
	Unique bool `json:"unique,omitempty" yaml:"unique,omitempty"`

	// AutoGenerated indicates the value is auto-generated by the database
	// (@autoGenerated).
	AutoGenerated bool `json:"autoGenerated,omitempty" yaml:"autoGenerated,omitempty"`

	// SearchField marks this field for inclusion in full-text trigram search
	// (@searchField).
	SearchField bool `json:"searchField,omitempty" yaml:"searchField,omitempty"`

	// JsonField marks object/list payloads to be stored in a single JSON
	// column (@jsonField).
	JsonField bool `json:"jsonField,omitempty" yaml:"jsonField,omitempty"`

	// Relation defines a foreign key relation to another type (@relation).
	// Nil if the field has no relation.
	Relation *RelationDef `json:"relation,omitempty" yaml:"relation,omitempty"`

	// HasMany indicates a one-to-many relationship (@hasMany).
	HasMany bool `json:"hasMany,omitempty" yaml:"hasMany,omitempty"`

	// ManyToMany indicates a many-to-many relationship requiring a join table
	// (@manyToMany).
	ManyToMany bool `json:"manyToMany,omitempty" yaml:"manyToMany,omitempty"`

	// Secret marks this field as sensitive data that should not be transmitted
	// to clients (@secret).
	Secret bool `json:"secret,omitempty" yaml:"secret,omitempty"`

	// Exclude hides an overlay field from the serving layer
	// (x-exclude): promote does not plan the column and the query catalog
	// never exposes it; ingestion still collects it into bronze.
	// Top-level only; nested exclude is rejected at write.
	Exclude bool `json:"exclude,omitempty" yaml:"exclude,omitempty"`

	// SemanticRole carries the existing x-semantic-role column annotation used
	// by quality rules (business_key, event_time, metadata_timestamp, etc.).
	SemanticRole string `json:"semanticRole,omitempty" yaml:"semanticRole,omitempty"`

	// TemporalFormat declares the wire encoding of a Temporal.DateTime field
	// whose source sends a bare epoch count instead of ISO text
	// (@temporalFormat / x-temporal-format). Values are the epoch members of
	// IncrementalTimeFormatEnum: unix, unix_millis, unix_micros, unix_nanos.
	// Empty means ISO text.
	TemporalFormat string `json:"temporalFormat,omitempty" yaml:"temporalFormat,omitempty"`

	// TransformDedupKey marks this field as part of the tap's business/dedup key
	// (@transformDedupKey / x-transformDedupKey).
	TransformDedupKey bool `json:"transformDedupKey,omitempty" yaml:"transformDedupKey,omitempty"`

	// TransformOrdering marks this field as the promote-path merge-ordering column
	// (@transformOrdering / x-transformOrdering). At most one per type.
	TransformOrdering bool `json:"transformOrdering,omitempty" yaml:"transformOrdering,omitempty"`

	// TransformFingerprintInput marks this field as an input to the change-detection
	// fingerprint (@transformFingerprintInput / x-transformFingerprintInput).
	TransformFingerprintInput bool `json:"transformFingerprintInput,omitempty" yaml:"transformFingerprintInput,omitempty"`

	// TransformPartitionDate marks this field as the partition / domain-date column
	// (@transformPartitionDate / x-transformPartitionDate). At most one per type.
	TransformPartitionDate bool `json:"transformPartitionDate,omitempty" yaml:"transformPartitionDate,omitempty"`

	// TransformStructural marks this field structural outside the derived roles
	// (@transformStructural / x-transformStructural).
	TransformStructural bool `json:"transformStructural,omitempty" yaml:"transformStructural,omitempty"`

	// TransformPersonEmail marks an email that identifies a real person
	// (@transformPersonEmail / x-transformPersonEmail). Identity role tag.
	TransformPersonEmail bool `json:"transformPersonEmail,omitempty" yaml:"transformPersonEmail,omitempty"`

	// TransformPersonName marks a human name component used for fuzzy matching
	// (@transformPersonName / x-transformPersonName). Identity role tag.
	TransformPersonName bool `json:"transformPersonName,omitempty" yaml:"transformPersonName,omitempty"`

	// TransformAccountId marks the connector-native account/user id - the row's
	// principal key (@transformAccountId / x-transformAccountId). Identity role tag.
	TransformAccountId bool `json:"transformAccountId,omitempty" yaml:"transformAccountId,omitempty"`

	// TransformExternalUserId marks a foreign principal id for another system
	// (@transformExternalUserId / x-transformExternalUserId). Identity role tag.
	TransformExternalUserId bool `json:"transformExternalUserId,omitempty" yaml:"transformExternalUserId,omitempty"`

	// TransformForeignKey marks an object-typed field as a foreign-key
	// reference to another tap (@transformForeignKey / x-transformForeignKey).
	// The promote path stores only the referenced id column. Nil when the
	// field is not a foreign key.
	TransformForeignKey *TransformForeignKeyDef `json:"transformForeignKey,omitempty" yaml:"transformForeignKey,omitempty"`

	// UIHidden marks this field as hidden in UI schema renderers (@uiHidden).
	UIHidden bool `json:"uiHidden,omitempty" yaml:"uiHidden,omitempty"`

	// InternalMetadata marks this field as internal codegen/dispatch metadata
	// (for example, a discriminator on a union variant) rather than
	// user-facing data (@internalMetadata).
	InternalMetadata bool `json:"internalMetadata,omitempty" yaml:"internalMetadata,omitempty"`

	// Auth indicates basic authentication is required to access this
	// field/operation (@auth).
	Auth bool `json:"auth,omitempty" yaml:"auth,omitempty"`

	// Public marks this field/operation as intentionally unauthenticated
	// (@public). Operations with @public are allowed in publicAPIRoutes
	// without @auth or @requirePermission.
	Public bool `json:"public,omitempty" yaml:"public,omitempty"`

	// Webhook marks an inbound webhook operation (@webhook): no
	// @auth / @requirePermission; registered on public routes with optional
	// @hmacVerified middleware.
	Webhook bool `json:"webhook,omitempty" yaml:"webhook,omitempty"`

	// HMACVerifiedProvider is the WebhookProviderEnum name from
	// @hmacVerified(provider: ...), or empty.
	HMACVerifiedProvider string `json:"hmacVerifiedProvider,omitempty" yaml:"hmacVerifiedProvider,omitempty"`

	// Encrypted marks this field/operation as requiring encrypted payload
	// transport (@encrypted).
	Encrypted bool `json:"encrypted,omitempty" yaml:"encrypted,omitempty"`

	// Filterable marks this endpoint as accepting bracket-notation filter
	// query parameters (@filterable). When true, the generated handler parses
	// filter[field]=value and filter[field][op]=value params.
	Filterable bool `json:"filterable,omitempty" yaml:"filterable,omitempty"`

	// RequireOwnership indicates the user can only access their own data
	// (@requireOwnership).
	RequireOwnership bool `json:"requireOwnership,omitempty" yaml:"requireOwnership,omitempty"`

	// Permissions lists required permissions for accessing this
	// field/operation (@requirePermission).
	Permissions []string `json:"permissions,omitempty" yaml:"permissions,omitempty"`

	// HTTPMethod is the explicit HTTP method for operation fields
	// ("GET", "POST", "PUT", "PATCH", "DELETE"). There is no
	// Queries-versus-Mutations default inference in v2: every operation
	// carries its method. Empty for non-operation fields.
	HTTPMethod string `json:"httpMethod,omitempty" yaml:"httpMethod,omitempty"`

	// RestMethod is retained for legacy runtime schema payloads.
	RestMethod string `json:"restMethod,omitempty" yaml:"restMethod,omitempty"`

	// RestPath overrides the auto-generated REST path segment for this
	// operation (@restPath).
	RestPath string `json:"restPath,omitempty" yaml:"restPath,omitempty"`

	// ParamType controls how arguments are mapped to REST parameters.
	// Value "query" means URL query string instead of path parameter (@query).
	ParamType string `json:"paramType,omitempty" yaml:"paramType,omitempty"`

	// HTTPBinaryStream configures an HTTP response as a binary stream with
	// explicit content type and optional response headers that must be
	// available at stream start (@httpBinaryStream).
	HTTPBinaryStream *HTTPBinaryStreamDef `json:"httpBinaryStream,omitempty" yaml:"httpBinaryStream,omitempty"`

	// ManualRouteRegistration marks an operation that should be registered
	// manually instead of the generated route registration chain
	// (@manualRouteRegistration).
	ManualRouteRegistration bool `json:"manualRouteRegistration,omitempty" yaml:"manualRouteRegistration,omitempty"`

	// Middleware holds field-level middleware overrides (nil means inherit
	// from the OperationSet).
	Middleware *MiddlewareConfig `json:"middleware,omitempty" yaml:"middleware,omitempty"`

	// Extensions holds extension decorator data keyed by extension name; see
	// [Schema.Extensions]. Operations are FieldDefs, so this is also the
	// slot for operation-level extension decorators.
	Extensions map[string]json.RawMessage `json:"extensions,omitempty" yaml:"extensions,omitempty"`
}

// HTTPBinaryStreamDef describes binary HTTP streaming response metadata for operations.
type HTTPBinaryStreamDef struct {
	// ContentType is the response Content-Type header value (for example,
	// "application/vnd.apache.arrow.stream").
	ContentType string `json:"contentType" yaml:"contentType"`

	// ResponseHeaders lists additional response headers that should be emitted
	// when the stream starts (for example, "X-Query-Id").
	ResponseHeaders []string `json:"responseHeaders,omitempty" yaml:"responseHeaders,omitempty"`
}

// TypeRef represents a reference to a named type with array and map metadata.
//
// Element nullability is expressed at the type level via Nullable<T> in the
// authoring formats; the IR does not carry a separate element-non-null flag.
type TypeRef struct {
	// Name is the base type name (scalar, enum, object type, union, etc.).
	Name string `json:"name" yaml:"name"`

	// IsArray indicates this is a list/array type.
	IsArray bool `json:"isArray,omitempty" yaml:"isArray,omitempty"`

	// ElemNonNull is retained for legacy runtime schema payloads.
	ElemNonNull bool `json:"elemNonNull,omitempty" yaml:"elemNonNull,omitempty"`

	// IsMap indicates this is a map/dictionary type with string keys.
	// When true, Name is the map value type and IsArray applies to that value.
	// IsMap and IsArray can both be true to represent Map<string, ValueType[]>.
	IsMap bool `json:"isMap,omitempty" yaml:"isMap,omitempty"`
}

// IndexDef represents a database index on a type.
type IndexDef struct {
	// Keys lists field names in index order.
	Keys []string `json:"keys" yaml:"keys"`

	// Unique controls whether the index is unique.
	Unique bool `json:"unique,omitempty" yaml:"unique,omitempty"`

	// Name is an optional purpose token used to build {idx|uq}_{table}_{name}.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`
}

// RelationDef represents a foreign key relation to another type.
type RelationDef struct {
	// Type is the related type name. If empty, it is inferred from the field
	// name (e.g., "orderId" infers relation to "Order").
	Type string `json:"type,omitempty" yaml:"type,omitempty"`

	// Field is the foreign key field name. If empty, it is inferred by the generator.
	Field string `json:"field,omitempty" yaml:"field,omitempty"`

	// OnDelete is the FK ON DELETE action (CASCADE|RESTRICT|NO ACTION). Empty
	// means the generator default (CASCADE).
	OnDelete string `json:"onDelete,omitempty" yaml:"onDelete,omitempty"`
}

// TransformForeignKeyDef captures the promote-path foreign-key reference for
// an object-typed field.
//
// In GraphQL: @transformForeignKey(tap: "repositories", idField: "id")
// In JSON Schema: "x-transformForeignKey": {"tap": "repositories", "idField": "id"}
type TransformForeignKeyDef struct {
	// Tap is the tap whose rows this field references.
	Tap string `json:"tap" yaml:"tap"`

	// IdField is the property of the referenced object that carries its id.
	// Defaults to "id".
	IdField string `json:"idField,omitempty" yaml:"idField,omitempty"`
}

// ArgumentDef represents an argument on a field or operation.
type ArgumentDef struct {
	// Name is the argument name.
	Name string `json:"name" yaml:"name"`

	// Description is the human-readable description.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Comment stores the node-attached comment for the argument declaration.
	Comment string `json:"comment,omitempty" yaml:"comment,omitempty"`

	// TypeRef references the argument's type.
	TypeRef TypeRef `json:"typeRef" yaml:"typeRef"`

	// Required indicates the argument is non-nullable.
	Required bool `json:"required,omitempty" yaml:"required,omitempty"`

	// Default is the default value (nil means no default).
	Default *string `json:"default,omitempty" yaml:"default,omitempty"`

	// ValidateMin is the minimum numeric value allowed for this argument
	// (@validateMin). Nil means no minimum.
	ValidateMin *float64 `json:"validateMin,omitempty" yaml:"validateMin,omitempty"`

	// ValidateMax is the maximum numeric value allowed for this argument
	// (@validateMax). Nil means no maximum.
	ValidateMax *float64 `json:"validateMax,omitempty" yaml:"validateMax,omitempty"`

	// ValidateMinLength is the minimum character length for string-like
	// arguments (@validateMinLength). Nil means no minimum.
	ValidateMinLength *int `json:"validateMinLength,omitempty" yaml:"validateMinLength,omitempty"`

	// ValidateMaxLength is the maximum character length for string-like
	// arguments (@validateMaxLength). Nil means no maximum.
	ValidateMaxLength *int `json:"validateMaxLength,omitempty" yaml:"validateMaxLength,omitempty"`

	// ValidateListMin is the minimum list cardinality (item count) for array
	// arguments (@validateListMin). Nil means no minimum.
	ValidateListMin *int `json:"validateListMin,omitempty" yaml:"validateListMin,omitempty"`

	// ValidateListMax is the maximum list cardinality (item count) for array
	// arguments (@validateListMax). Nil means no maximum.
	ValidateListMax *int `json:"validateListMax,omitempty" yaml:"validateListMax,omitempty"`

	// ValidatePattern is the regex pattern enforced for this argument
	// (@validatePattern). Empty string means no pattern.
	ValidatePattern string `json:"validatePattern,omitempty" yaml:"validatePattern,omitempty"`

	// IsQuery indicates this argument should be a URL query parameter instead
	// of a path parameter (@query).
	IsQuery bool `json:"isQuery,omitempty" yaml:"isQuery,omitempty"`
}
