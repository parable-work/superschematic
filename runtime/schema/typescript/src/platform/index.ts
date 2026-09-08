import {
  normalizeContactEmail,
  normalizeContactPhoneNumber,
  normalizeDesignColor,
  normalizeGenericJSON,
  normalizeIdentityUUID,
  normalizeTemporalDate,
  normalizeTemporalDateTime,
  normalizeTemporalDuration,
  normalizeTemporalMonth,
  normalizeTemporalQuarterYear,
  parseContactEmail,
  parseContactPhoneNumber,
  parseDesignColor,
  parseGenericJSON,
  parseGeoLocation,
  parseIdentityUUID,
  parseParablePermission,
  parseTemporalDate,
  parseTemporalDateTime,
  parseTemporalDuration,
  parseTemporalMonth,
  parseTemporalQuarterYear,
  validateContactEmail,
  validateContactPhoneNumber,
  validateDesignColor,
  validateGenericJSON,
  validateGeoLocation,
  validateIdentityUUID,
  validateParablePermission,
  validateTemporalDate,
  validateTemporalDateTime,
  validateTemporalDuration,
  validateTemporalMonth,
  validateTemporalQuarterYear,
} from '@psgen/scalar-lib/scalars';
import type {
  ArtifactFileMetadata,
  FileMetadata,
  ImageMetadata,
  LogoImageMetadata,
  ParableSchemaDataType,
  ParableSchemaType,
} from '@psgen/scalar-lib/scalars';
import { normalizePemNewlines } from '@psgen/scalar-lib/pem';
import type { ScalarValidationResult, ValidationError } from '@psgen/scalar-lib/validation';
import { toScalarResult } from '@psgen/scalar-lib/validation';
import type { Schema } from '../runtime/validation/types';
import {
  getPermissionDescription,
  PermissionDescriptions,
  PermissionEnum,
  type Permission,
} from '@psgen/scalar-lib/permissions';

export {
  getPermissionDescription,
  PermissionDescriptions,
  PermissionEnum,
  type Permission,
};
export { normalizePemNewlines } from '@psgen/scalar-lib/pem';

// The object-scalar metadata shapes are owned by @psgen/scalar-lib (its
// generated module types Asset.File, Asset.Image, Asset.LogoImage,
// Artifact.File, Parable.Schema and Parable.SchemaData with them); they are
// re-exported here so the validators facade keeps its public surface.
export type {
  ArtifactFileMetadata,
  FileMetadata,
  ImageMetadata,
  LogoImageMetadata,
  ParableSchemaDataType,
  ParableSchemaType,
} from '@psgen/scalar-lib/scalars';

export interface LogoImageConstraints {
  maxWidth?: number;
  maxHeight?: number;
  minAspectRatio?: number;
  maxAspectRatio?: number;
  requireTransparency?: boolean;
}

export interface LocationValue {
  lat: number;
  lon: number;
}

export const DefaultMaxFileSize = 50 * 1024 * 1024;
export const AllowedImageMimeTypes: ReadonlySet<string> = new Set([
  'image/jpeg',
  'image/png',
  'image/gif',
  'image/webp',
]);
export const DefaultMaxImageSize = 10 * 1024 * 1024;
export const AllowedLogoImageMimeTypes: ReadonlySet<string> = new Set(['image/png']);
export const DefaultMaxLogoImageSize = 5 * 1024 * 1024;

export const PARABLE_SLUG_MAX_LENGTH = 17;
export const PARABLE_SLUG_SUFFIX_LENGTH = 5;
export const PARABLE_SLUG_BASE_MAX_LENGTH =
  PARABLE_SLUG_MAX_LENGTH - PARABLE_SLUG_SUFFIX_LENGTH;

const PermissionPattern = /^[a-z][a-z0-9]*(?:\.[a-z][a-z0-9]*)*$/;
const permissionValues = new Set<string>(Object.values(PermissionEnum));

function hasString(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isRuntimeSchema(value: unknown): value is Schema {
  return (
    isObject(value) &&
    isObject(value.scalars) &&
    isObject(value.enums) &&
    isObject(value.types) &&
    isObject(value.inputs)
  );
}

function errorsForFileLike(
  value: { mimeType?: unknown; size?: unknown; filename?: unknown } | null | undefined,
  label: string
): ValidationError[] {
  const errors: ValidationError[] = [];
  if (value == null) {
    errors.push({ validator: 'required', message: `${label} metadata is required` });
    return errors;
  }
  if (!isObject(value)) {
    errors.push({ validator: 'format', message: `${label} metadata must be an object` });
    return errors;
  }
  if (!value.mimeType || typeof value.mimeType !== 'string') {
    errors.push({ validator: 'required', message: 'mimeType is required' });
  }
  if (typeof value.size !== 'number' || value.size <= 0) {
    errors.push({ validator: 'minimum', message: 'size must be greater than 0' });
  }
  if (!value.filename || typeof value.filename !== 'string') {
    errors.push({ validator: 'required', message: 'filename is required' });
  }
  return errors;
}

function pushUrlError(errors: ValidationError[], url: unknown): void {
  if (!url || typeof url !== 'string') {
    errors.push({ validator: 'required', message: 'url is required' });
    return;
  }
  try {
    new URL(url);
  } catch {
    errors.push({ validator: 'format', message: 'url must be a valid URL' });
  }
}

export function validateFile(value: FileMetadata | null | undefined): ScalarValidationResult {
  const errors = errorsForFileLike(value, 'file');
  if (value && isObject(value) && value.url) {
    try {
      new URL(value.url);
    } catch {
      errors.push({ validator: 'format', message: 'url must be a valid URL' });
    }
  }
  return toScalarResult(errors);
}

export function validateFileForUpload(value: FileMetadata | null | undefined): ValidationError[] {
  return errorsForFileLike(value, 'file');
}

export function validateFileWithAllowedTypes(
  value: FileMetadata | null | undefined,
  allowedTypes: string[]
): ValidationError[] {
  const [, baseErrors] = validateFile(value);
  const errors: ValidationError[] = baseErrors ? [...baseErrors] : [];
  if (value && value.mimeType && allowedTypes.length > 0) {
    const isAllowed = allowedTypes.some(
      type => type.toLowerCase() === value.mimeType.toLowerCase()
    );
    if (!isAllowed) {
      errors.push({
        validator: 'enum',
        message: `mimeType '${value.mimeType}' is not allowed`,
      });
    }
  }
  return errors;
}

export function isFileEmpty(meta: FileMetadata | null | undefined): boolean {
  if (meta == null) {
    return true;
  }
  return !meta.assetId && !meta.url && !meta.mimeType && meta.size === 0 && !meta.filename;
}

export function createFileMetadata(
  url: string,
  mimeType: string,
  filename: string,
  size: number
): FileMetadata {
  return { url, mimeType, size, filename };
}

export function createFileMetadataForUpload(
  mimeType: string,
  filename: string,
  size: number
): FileMetadata {
  return { url: '', mimeType, size, filename };
}

export function isFileMetadata(value: unknown): value is FileMetadata {
  return (
    isObject(value) &&
    typeof value.url === 'string' &&
    typeof value.mimeType === 'string' &&
    typeof value.size === 'number' &&
    typeof value.filename === 'string'
  );
}

export function validateImage(
  value: ImageMetadata | File | null | undefined
): ScalarValidationResult {
  const errors: ValidationError[] = [];
  if (value == null) {
    return [false, [{ validator: 'required', message: 'image metadata is required' }]];
  }
  if (value instanceof File) {
    if (!AllowedImageMimeTypes.has(value.type.toLowerCase())) {
      errors.push({
        validator: 'enum',
        message: 'file type must be one of: image/jpeg, image/png, image/gif, image/webp',
      });
    }
    if (value.size <= 0) {
      errors.push({ validator: 'minimum', message: 'file size must be greater than 0' });
    } else if (value.size > DefaultMaxImageSize) {
      errors.push({
        validator: 'maximum',
        message: `file size must be less than ${DefaultMaxImageSize / (1024 * 1024)}MB`,
      });
    }
    return toScalarResult(errors);
  }
  if (!isObject(value)) {
    return [false, [{ validator: 'format', message: 'image metadata must be an object' }]];
  }
  pushUrlError(errors, value.url);
  if (!value.mimeType || typeof value.mimeType !== 'string') {
    errors.push({ validator: 'required', message: 'mimeType is required' });
  } else if (!AllowedImageMimeTypes.has(value.mimeType.toLowerCase())) {
    errors.push({
      validator: 'enum',
      message: 'mimeType must be one of: image/jpeg, image/png, image/gif, image/webp',
    });
  }
  if (typeof value.size !== 'number' || value.size <= 0) {
    errors.push({ validator: 'minimum', message: 'size must be greater than 0' });
  }
  if (typeof value.width !== 'number' || value.width <= 0) {
    errors.push({ validator: 'minimum', message: 'width must be greater than 0' });
  }
  if (typeof value.height !== 'number' || value.height <= 0) {
    errors.push({ validator: 'minimum', message: 'height must be greater than 0' });
  }
  if (!value.filename || typeof value.filename !== 'string') {
    errors.push({ validator: 'required', message: 'filename is required' });
  }
  return toScalarResult(errors);
}

export function isImageEmpty(meta: ImageMetadata | null | undefined): boolean {
  if (meta == null) {
    return true;
  }
  return (
    !meta.assetId &&
    !meta.url &&
    !meta.mimeType &&
    meta.size === 0 &&
    meta.width === 0 &&
    meta.height === 0 &&
    !meta.filename
  );
}

export function createImageMetadata(
  url: string,
  mimeType: string,
  filename: string,
  size: number,
  width: number,
  height: number
): ImageMetadata {
  return { url, mimeType, size, width, height, filename };
}

export function isImageMetadata(value: unknown): value is ImageMetadata {
  return (
    isObject(value) &&
    typeof value.url === 'string' &&
    typeof value.mimeType === 'string' &&
    typeof value.size === 'number' &&
    typeof value.width === 'number' &&
    typeof value.height === 'number' &&
    typeof value.filename === 'string'
  );
}

export function defaultLogoImageConstraints(): LogoImageConstraints {
  return { requireTransparency: true };
}

export function validateLogoImage(
  value: LogoImageMetadata | File | null | undefined
): ScalarValidationResult {
  const errors: ValidationError[] = [];
  if (value == null) {
    return [false, [{ validator: 'required', message: 'logo image metadata is required' }]];
  }
  if (value instanceof File) {
    if (!AllowedLogoImageMimeTypes.has(value.type.toLowerCase())) {
      errors.push({ validator: 'enum', message: 'file type must be image/png for logo images' });
    }
    if (value.size <= 0) {
      errors.push({ validator: 'minimum', message: 'file size must be greater than 0' });
    } else if (value.size > DefaultMaxLogoImageSize) {
      errors.push({
        validator: 'maximum',
        message: `file size must be less than ${DefaultMaxLogoImageSize / (1024 * 1024)}MB`,
      });
    }
    return toScalarResult(errors);
  }
  if (!isObject(value)) {
    return [false, [{ validator: 'format', message: 'logo image metadata must be an object' }]];
  }
  pushUrlError(errors, value.url);
  if (!value.mimeType || typeof value.mimeType !== 'string') {
    errors.push({ validator: 'required', message: 'mimeType is required' });
  } else if (!AllowedLogoImageMimeTypes.has(value.mimeType.toLowerCase())) {
    errors.push({ validator: 'enum', message: 'mimeType must be image/png for logo images' });
  }
  if (typeof value.size !== 'number' || value.size <= 0) {
    errors.push({ validator: 'minimum', message: 'size must be greater than 0' });
  }
  if (typeof value.width !== 'number' || value.width <= 0) {
    errors.push({ validator: 'minimum', message: 'width must be greater than 0' });
  }
  if (typeof value.height !== 'number' || value.height <= 0) {
    errors.push({ validator: 'minimum', message: 'height must be greater than 0' });
  }
  if (!value.filename || typeof value.filename !== 'string') {
    errors.push({ validator: 'required', message: 'filename is required' });
  }
  return toScalarResult(errors);
}

export function validateLogoImageForUpload(
  value: LogoImageMetadata | null | undefined
): ValidationError[] {
  const errors = errorsForFileLike(value, 'logo image');
  if (value && value.mimeType && !AllowedLogoImageMimeTypes.has(value.mimeType.toLowerCase())) {
    errors.push({ validator: 'enum', message: 'mimeType must be image/png for logo images' });
  }
  if (value && typeof value.width !== 'number') {
    errors.push({ validator: 'minimum', message: 'width must be greater than 0' });
  }
  if (value && typeof value.height !== 'number') {
    errors.push({ validator: 'minimum', message: 'height must be greater than 0' });
  }
  return errors;
}

export function validateLogoImageWithConstraints(
  value: LogoImageMetadata | null | undefined,
  constraints: LogoImageConstraints
): ValidationError[] {
  const [, baseErrors] = validateLogoImage(value);
  const errors: ValidationError[] = baseErrors ? [...baseErrors] : [];
  if (value == null) {
    return errors;
  }
  if (constraints.maxWidth && constraints.maxWidth > 0 && value.width > constraints.maxWidth) {
    errors.push({
      validator: 'maxWidth',
      message: `width ${value.width} exceeds maximum allowed width ${constraints.maxWidth}`,
    });
  }
  if (constraints.maxHeight && constraints.maxHeight > 0 && value.height > constraints.maxHeight) {
    errors.push({
      validator: 'maxHeight',
      message: `height ${value.height} exceeds maximum allowed height ${constraints.maxHeight}`,
    });
  }
  if (value.width > 0 && value.height > 0) {
    const aspectRatio = value.width / value.height;
    if (
      constraints.minAspectRatio &&
      constraints.minAspectRatio > 0 &&
      aspectRatio < constraints.minAspectRatio
    ) {
      errors.push({
        validator: 'minAspectRatio',
        message: `aspect ratio ${aspectRatio.toFixed(2)} is below minimum ${constraints.minAspectRatio.toFixed(2)}`,
      });
    }
    if (
      constraints.maxAspectRatio &&
      constraints.maxAspectRatio > 0 &&
      aspectRatio > constraints.maxAspectRatio
    ) {
      errors.push({
        validator: 'maxAspectRatio',
        message: `aspect ratio ${aspectRatio.toFixed(2)} exceeds maximum ${constraints.maxAspectRatio.toFixed(2)}`,
      });
    }
  }
  if (constraints.requireTransparency && !value.hasTransparency) {
    errors.push({
      validator: 'requireTransparency',
      message: 'logo image must have transparency (alpha channel)',
    });
  }
  return errors;
}

export function isLogoImageEmpty(meta: LogoImageMetadata | null | undefined): boolean {
  if (meta == null) {
    return true;
  }
  return (
    !meta.assetId &&
    !meta.url &&
    !meta.mimeType &&
    meta.size === 0 &&
    meta.width === 0 &&
    meta.height === 0 &&
    !meta.filename
  );
}

export function createLogoImageMetadata(
  url: string,
  mimeType: string,
  filename: string,
  size: number,
  width: number,
  height: number,
  hasTransparency: boolean
): LogoImageMetadata {
  return { url, mimeType, size, width, height, filename, hasTransparency };
}

export function createLogoImageMetadataForUpload(
  mimeType: string,
  filename: string,
  size: number,
  width: number,
  height: number,
  hasTransparency: boolean
): LogoImageMetadata {
  return { url: '', mimeType, size, width, height, filename, hasTransparency };
}

export function isLogoImageMetadata(value: unknown): value is LogoImageMetadata {
  return (
    isObject(value) &&
    typeof value.url === 'string' &&
    typeof value.mimeType === 'string' &&
    typeof value.size === 'number' &&
    typeof value.width === 'number' &&
    typeof value.height === 'number' &&
    typeof value.filename === 'string' &&
    typeof value.hasTransparency === 'boolean'
  );
}

export function getLogoImageAspectRatio(meta: LogoImageMetadata): number {
  if (meta.width === 0 || meta.height === 0) {
    return 0;
  }
  return meta.width / meta.height;
}

export function validateArtifactFile(
  value: ArtifactFileMetadata | null | undefined
): ScalarValidationResult {
  const errors = errorsForFileLike(value, 'artifact file');
  return toScalarResult(errors);
}

export function validateArtifactFileForUpload(
  value: ArtifactFileMetadata | null | undefined
): ScalarValidationResult {
  return validateArtifactFile(value);
}

export function isArtifactFileEmpty(meta: ArtifactFileMetadata | null | undefined): boolean {
  if (meta == null) {
    return true;
  }
  return !meta.gcsPath && !meta.mimeType && meta.size === 0 && !meta.filename;
}

export function normalizeParableSlugBase(
  input: string,
  maxLength: number = PARABLE_SLUG_BASE_MAX_LENGTH
): string {
  const folded = input
    .normalize('NFKD')
    .replace(/\p{Mn}/gu, '')
    .normalize('NFC');
  let result = '';
  let prevDash = true;
  for (const ch of folded) {
    const code = ch.codePointAt(0);
    if (code != null && code >= 0x41 && code <= 0x5a) {
      result += String.fromCodePoint(code + 0x20);
      prevDash = false;
    } else if (
      code != null &&
      ((code >= 0x61 && code <= 0x7a) || (code >= 0x30 && code <= 0x39))
    ) {
      result += ch;
      prevDash = false;
    } else if (!prevDash) {
      result += '-';
      prevDash = true;
    }
  }
  let slug = result.replace(/^-+|-+$/g, '');
  if (slug === '') {
    throw new Error('parable slug: empty after normalization');
  }
  if (slug.length > maxLength) {
    slug = slug.slice(0, maxLength).replace(/-+$/, '');
    if (slug === '') {
      throw new Error('parable slug: empty after normalization');
    }
  }
  return slug;
}

export function normalizeParableSlug(input: string): string {
  return normalizeParableSlugBase(input);
}

export function parseEmail(input: undefined | null | string): string | null {
  if (input === undefined || input === null || typeof input !== 'string') {
    return null;
  }
  return parseContactEmail(input);
}

export function normalizeEmail(input: string): string | null {
  return normalizeContactEmail(input);
}

export function validateEmail(input: undefined | null | string): ScalarValidationResult {
  return validateContactEmail(input);
}

export function parseUUID(input: undefined | null | string): string | null {
  if (!hasString(input)) {
    return null;
  }
  return parseIdentityUUID(input);
}

export function normalizeUUID(input: string): string | null {
  return normalizeIdentityUUID(input);
}

export function validateUUID(input: undefined | null | string): ScalarValidationResult {
  return validateIdentityUUID(input);
}

export const parseColor = parseDesignColor;
export const normalizeColor = normalizeDesignColor;
export const validateColor = validateDesignColor;
export const parsePhoneNumber = parseContactPhoneNumber;
export const normalizePhoneNumber = normalizeContactPhoneNumber;
export const validatePhoneNumber = validateContactPhoneNumber;
export const parseDate = parseTemporalDate;
export const normalizeDate = normalizeTemporalDate;
export const validateDate = validateTemporalDate;
export const parseMonth = parseTemporalMonth;
export const normalizeMonth = normalizeTemporalMonth;
export const validateMonth = validateTemporalMonth;
export const parseQuarterYear = parseTemporalQuarterYear;
export const normalizeQuarterYear = normalizeTemporalQuarterYear;
export const validateQuarterYear = validateTemporalQuarterYear;
export const parseJSON = parseGenericJSON;
export const normalizeJSON = normalizeGenericJSON;
export const validateJSON = validateGenericJSON;
export const parseLocation = parseGeoLocation;
export const validateLocation = validateGeoLocation;
export const parseDuration = parseTemporalDuration;
export const normalizeDuration = normalizeTemporalDuration;
export const validateDuration = validateTemporalDuration;
export const parseDateTime = parseTemporalDateTime;
export const normalizeDateTime = normalizeTemporalDateTime;
export const validateDateTime = validateTemporalDateTime;

export function parsePermission(input: undefined | null | string): string | null {
  if (input === undefined || input === null || typeof input !== 'string') {
    return null;
  }
  const trimmed = input.trim();
  if (trimmed === '' || !permissionValues.has(trimmed)) {
    return null;
  }
  return trimmed;
}

export function validatePermission(input: undefined | null | string): ScalarValidationResult {
  const generated = validateParablePermission(input);
  if (!generated[0]) {
    return generated;
  }
  if (input === undefined || input === null || typeof input !== 'string') {
    return [false, [{ validator: 'format', message: 'must be a valid Permission' }]];
  }
  const errors: ValidationError[] = [];
  if (input.length > 255) {
    errors.push({ validator: 'maxLength', message: 'must be at most 255 characters' });
  }
  if (!PermissionPattern.test(input)) {
    errors.push({
      validator: 'pattern',
      message: 'invalid permission format - must be lowercase alphanumeric with dots',
    });
  }
  if (!permissionValues.has(input)) {
    errors.push({ validator: 'pattern', message: 'invalid permission value' });
  }
  return toScalarResult(errors);
}

function decodePemMarker(b64: string): string {
  if (typeof globalThis.atob === 'function') {
    return globalThis.atob(b64);
  }
  if (typeof Buffer !== 'undefined') {
    return Buffer.from(b64, 'base64').toString('utf8');
  }
  return '';
}

const PKCS8_BEGIN = decodePemMarker('LS0tLS1CRUdJTiBQUklWQVRFIEtFWS0tLS0t');
const PKCS8_END = decodePemMarker('LS0tLS1FTkQgUFJJVkFURSBLRVktLS0tLQ==');
const RSA_BEGIN = decodePemMarker('LS0tLS1CRUdJTiBSU0EgUFJJVkFURSBLRVktLS0tLQ==');
const RSA_END = decodePemMarker('LS0tLS1FTkQgUlNBIFBSSVZBVEUgS0VZLS0tLS0=');
const MAX_PEM_LENGTH = 64 * 1024;

type PemMarkers = {
  bodyStart: number;
  endStart: number;
  endLen: number;
};

function findPemMarkers(pem: string): PemMarkers | null {
  if (pem.startsWith(RSA_BEGIN)) {
    const endStart = pem.indexOf(RSA_END, RSA_BEGIN.length);
    if (endStart === -1) {
      return null;
    }
    return { bodyStart: RSA_BEGIN.length, endStart, endLen: RSA_END.length };
  }
  if (pem.startsWith(PKCS8_BEGIN)) {
    const endStart = pem.indexOf(PKCS8_END, PKCS8_BEGIN.length);
    if (endStart === -1) {
      return null;
    }
    return { bodyStart: PKCS8_BEGIN.length, endStart, endLen: PKCS8_END.length };
  }
  return null;
}

function isValidPemEnvelope(pem: string): boolean {
  if (pem.length > MAX_PEM_LENGTH) {
    return false;
  }
  const markers = findPemMarkers(pem);
  if (!markers || markers.endStart <= markers.bodyStart) {
    return false;
  }
  const afterEnd = pem.slice(markers.endStart + markers.endLen);
  for (let i = 0; i < afterEnd.length; i += 1) {
    const code = afterEnd.charCodeAt(i);
    if (code !== 0x20 && code !== 0x09 && code !== 0x0a && code !== 0x0d) {
      return false;
    }
  }
  return true;
}

function decodeBase64PemBody(pem: string): Uint8Array | null {
  const markers = findPemMarkers(pem);
  if (!markers) {
    return null;
  }
  const body = pem.slice(markers.bodyStart, markers.endStart);
  let base64 = '';
  for (let i = 0; i < body.length; i += 1) {
    const ch = body[i];
    if (ch !== ' ' && ch !== '\t' && ch !== '\n' && ch !== '\r') {
      base64 += ch;
    }
  }
  if (base64.length === 0) {
    return null;
  }
  try {
    if (typeof globalThis.atob === 'function') {
      const binary = globalThis.atob(base64);
      const bytes = new Uint8Array(binary.length);
      for (let i = 0; i < binary.length; i += 1) {
        bytes[i] = binary.charCodeAt(i);
      }
      return bytes;
    }
    if (typeof Buffer !== 'undefined') {
      return new Uint8Array(Buffer.from(base64, 'base64'));
    }
  } catch {
    return null;
  }
  return null;
}

function hasDecodablePemBody(pem: string): boolean {
  const der = decodeBase64PemBody(pem);
  return der !== null && der.length >= 64;
}

export function validateCryptoRSAPrivateKey(
  input: undefined | null | string
): ScalarValidationResult {
  if (input === undefined || input === null || typeof input !== 'string') {
    return [
      false,
      [{ validator: 'format', message: 'must be a valid RSA private key in PEM format' }],
    ];
  }
  const normalized = normalizePemNewlines(input.trim());
  if (normalized === '' || !isValidPemEnvelope(normalized) || !hasDecodablePemBody(normalized)) {
    return [
      false,
      [{ validator: 'format', message: 'must be a valid RSA private key in PEM format' }],
    ];
  }
  return [true, null];
}

export const validateRSAPrivateKey = validateCryptoRSAPrivateKey;

export function validateParableSchema(value: ParableSchemaType): ScalarValidationResult {
  if (!isObject(value)) {
    return [false, [{ validator: 'required', message: 'ParableSchema must be an object' }]];
  }
  if (!isRuntimeSchema(value)) {
    return [
      false,
      [{ validator: 'required', message: 'ParableSchema must be a valid runtime schema' }],
    ];
  }
  return [true, null];
}

export function validateParableSchemaData(value: ParableSchemaDataType): ScalarValidationResult {
  if (!isObject(value)) {
    return [false, [{ validator: 'required', message: 'ParableSchemaData must be an object' }]];
  }
  return [true, null];
}
