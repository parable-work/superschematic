import { Ext } from "@fixture/ext-scalars";
import { Generic } from "superscalar";

// A reading: a payload of the extension's JSON object scalar, and free-form
// metadata.
export abstract class Reading {
  payload: Ext.Doc;

  meta: Generic.JSON;
}
