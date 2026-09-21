# Browse and find photos

Use the web app to find photos, organize albums, and download original files.
Open the URL printed by `fotobank daemon start`. If your library is empty,
[import a folder](import.md) on the machine running Fotobank first.

## Browse your library

Open **Library** to browse photos, or **Sessions** to explore capture sessions.
Sessions starts independently of Library filters. Open a photo to inspect it;
ready thumbnails appear as background processing finishes. A missing preview
does not mean the original is missing. Check [files and previews](formats.md)
for format limits.

On a phone, photos use the full width of the screen. Open **Browse & filters**
to reach the sidebar. Changing a filter keeps that panel open so you can make
several choices; close it to return to your photos.

If Library, Sessions, or Search cannot load a page, use **Retry**. Loaded photos,
the query, and filters remain in place. A failed request shows an error instead
of claiming that the library has no matching photos.

## Search and narrow the results

Search filenames, camera and lens information, tags, captions, and location
labels without enabling AI. Optional [AI search](ai.md#add-semantic-search-separately)
can also match images by meaning when embeddings are available.

Open **Filters** to narrow the results. Sorting and active filters stay visible
when you close the panel, and you can remove an active filter without reopening
it. Filter choices remain in the URL when you reload the page. Continue browsing
to load more results; pagination stops at the last page.

Search and Map are for browsing. To select photos for an action, use Library,
Sessions, an album, or Hidden.

## Select photos and manage albums

Use a photo's checkbox to select it without opening it. Library, Sessions,
Albums, and Hidden support checkbox selection by touch or keyboard.
Ctrl/Cmd-click and Shift-click selection are also available in these views.

Albums group photos without making another copy. Removing a photo from an
album or deleting the album leaves the photos in the library. If some removals
fail, those photos stay selected so you can retry. If album deletion fails,
the album stays open and shows the error. An outstanding share can block
deletion; revoke that share first.

For scripts, use [album commands](automation.md#organize-albums).

## Download a photo or a related file

Photo details offer the primary original and its attached RAW or XMP files.
A photo with no attachments still has a primary-file download. See
[related files](formats.md#how-are-related-files-grouped) for grouping rules.

A download is an independent copy. To save edits back into Fotobank as new
versions, [create a checkout](checkouts.md). For a download that verifies size
and checksum, use [the CLI](automation.md#download-an-original-or-attachment).

## Find privacy, AI, and workflow help

Open **Settings** for **Hidden photos**, **AI processing**, and links to import,
editing, and backup guides. Hidden photos require an unlock; hiding does not
encrypt their files. The [AI guide](ai.md) explains provider setup and consent.

Sharing with another person needs an external identity proxy and broker.
The default single-user setup does not publish shares remotely, and sharing
controls are hidden by default. Read [sharing setup and limits](automation.md#manage-sharing)
before enabling them.
