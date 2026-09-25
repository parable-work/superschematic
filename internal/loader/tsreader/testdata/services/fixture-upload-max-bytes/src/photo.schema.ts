import { Validate } from "@superschematic/schema";
import { Media } from "./media";

// uploadMaxBytes caps a file-upload scalar's multipart part below the
// scalar's own limit.
export abstract class PhotoUpload {
  photo: Validate<Media.Photo, { uploadMaxBytes: 1048576 }>;
  caption: string;
}
