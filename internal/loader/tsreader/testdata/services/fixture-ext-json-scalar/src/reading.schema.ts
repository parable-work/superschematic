import { Generic } from "superscalar";
import { Ext } from "./ext";

// A reading: a payload of the extension's JSON object scalar, and free-form
// metadata.
export abstract class Reading {
  payload: Ext.Doc;
  meta: Generic.JSON;
}
