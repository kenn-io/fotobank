# Fotobank: A database for your digital photographs

Managing a lifetime's worth of digital photos can be stressful. This is a
simple tool to create and maintain an orderly collection of all your digital
imagery. It is also independent of any paid products.

Among other things, Fotobank will:

* Deduplicate photos on import
* Give files reasonable, consistent names containing the date and time
* Maintain a metadata database allowing for easy analytics and visualizations
  about your photo collection

Your Fotobank database can be relocated easily and backed up. This software is
implemented in Python and has been tested with Python 3.6.

## Installation and Quickstart

Fotobank depends on [exiftool][1] a Perl-based command line application for
reading the EXIF metadata from a wide variety of digital media, such as JPEG
images, RAW files for most cameras, movies, and more. First, install that.

To install Fotobank itself, run:

```shell
uv add fotobank
```

Or if you prefer pip:

```shell
pip install fotobank
```

### Local Development

For local development, clone this repository and run:

```shell
uv sync
```

Then run fotobank commands with:

```shell
uv run fotobank [command]
```

The primary command for Fotobank is `import`. To begin adding photos to your
database, first create a folder where you want to store the database, such as:

```shell
mkdir ~/photo_archive
```

Now, suppose we have some photos stored in `~/old_photos`. This can be
imported with the command:

```shell
fotobank import -d ~/photo_archive ~/old_photos
```

By default, `fotobank` copies the photos from `~/old_photos` into the new
database. Photos are grouped by year

If you would like to omit the `-d` option, you can set the environment variable
`FOTOBANK_DEFAULT_PATH`.

## Using with Adobe Lightroom Classic

If you are using Fotobank and Lightroom (LR) together, you should stick to
importing images from memory cards or other locations with the `fotobank`
command-line interface.

The simplest way to keep LR up to date is to add the root directory where your
database is stored, and instruct Lightroom to synchronize with the directory
contents. This way, the next time you run `fotobank import` the new photos will
automatically show up.

It would be a good idea to colocate your Lighroom catalog (which contains
LR-specific metadata and photo edits) with your Fotobank database.

## Deleting photos and keeping clean metadata

If you delete photos from your Fotobank database (e.g. using Lightroom), its
metadata registry will fall out of sync. To fix this, run the command:

```
fotobank sync -d $DATABASE_PATH
```

Here you should replace `$DATABASE_PATH` with the actual path to your photos.

This will scan all of the photo metadata stored and remove any entries
referring to images that have been deleted.

## Repairing and editing image metadata

TODO. We want to be able to alter EXIF metadata easily (create date, make, model, etc.)

## Feature roadmap

* Transactional metadata changes / undo functionality
* Merging multiple Fotobank databases into one
* Splitting Fotobank into smaller chunks (e.g. by year) for cold storage
* Web UI with analytics and visualizations

[1]: https://www.sno.phy.queensu.ca/~phil/exiftool/