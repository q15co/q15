import { Download } from "lucide-react";
import { useContext, useEffect, useState } from "react";

import type { MediaObject } from "../application/ports";
import type { Part } from "../generated/protocol";

import { inlineAudio, inlineImages, isMediaKind, mediaTreatments } from "../domain/media";
import { FileIcon } from "./file-icon";
import { MediaContext } from "./media-context";

import styles from "./media.module.css";

interface MediaProps {
  part: Part;
  filename?: string;
}
function InlineMedia({ kind, media }: { kind: string; media: MediaObject }) {
  const treatment = isMediaKind(kind) ? mediaTreatments[kind] : "download";
  if (treatment === "image" && inlineImages.includes(media.contentType))
    return <img className={styles.image} src={media.url} alt={media.filename} />;
  if (treatment === "audio" && inlineAudio.includes(media.contentType))
    // oxlint-disable-next-line jsx-a11y/media-has-caption -- Uploaded audio has no caption track.
    return <audio className={styles.audio} controls src={media.url} aria-label={media.filename} />;
  return null;
}
export function MediaView(props: MediaProps) {
  return <MediaContent key={props.part.media_ref ?? ""} {...props} />;
}
function MediaContent({ part, filename }: MediaProps) {
  const media = useContext(MediaContext);
  const [loaded, setLoaded] = useState<MediaObject>();
  const [error, setError] = useState(false);
  const ref = part.media_ref ?? "";
  useEffect(() => {
    const controller = new AbortController();
    let object: MediaObject | undefined;
    if (media && ref !== "")
      void media
        .load(ref, controller.signal)
        .then((value) => {
          object = value;
          if (controller.signal.aborted) value.dispose();
          else setLoaded(value);
          return null;
        })
        .catch(() => {
          if (!controller.signal.aborted) setError(true);
        });
    return () => {
      controller.abort();
      object?.dispose();
    };
  }, [media, ref]);
  const kind = part.media_kind ?? "Media";
  const name = loaded?.filename ?? filename ?? "Attachment";
  return (
    <div className={styles.attachment}>
      <div className={styles.header}>
        <span className={styles.icon}>
          <FileIcon filename={name} contentType={loaded?.contentType ?? ""} />
        </span>
        <div className={styles.caption}>
          <strong title={name}>{name}</strong>
          <small>{kind.replaceAll("_", " ")}</small>
        </div>
      </div>
      {loaded && <InlineMedia kind={kind} media={loaded} />}
      {error && <output className={styles.error}>Attachment unavailable.</output>}
      {loaded && (
        <a className={styles.download} href={loaded.url} download={loaded.filename}>
          <Download size={14} aria-hidden="true" />
          Download
        </a>
      )}
    </div>
  );
}
