// Discriminated union of share targets the modal accepts.
// Task 18/19 components construct one of these and pass it as the
// `target` prop to ShareModal.
import type { Grantee } from "../format/parseGrantee";

export type ShareTarget =
  | { type: "media_set"; mediaIds: string[] }
  | { type: "album_live"; albumId: string; albumName: string };

// Discriminated body shape POSTed to /api/v1/shares. Mirrors
// internal/httpapi/shares.go::createShareInput.Body.
export type CreateShareBody =
  | {
      target_type: "media_set";
      media_ids: string[];
      grantee: Grantee;
      label: string;
      allow_download: boolean;
    }
  | {
      target_type: "album_live";
      album_id: string;
      grantee: Grantee;
      label: string;
      allow_download: boolean;
    };
