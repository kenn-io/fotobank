from datetime import datetime
import json
import logging
import os
import shutil
import subprocess

import dateutil.parser as date_parser
import exifread

import fotobank.util as util

logger = logging.getLogger(__name__)

# Check if exiftool is available on the system
_EXIFTOOL_AVAILABLE = shutil.which('exiftool') is not None


class ImageMetadata(object):
    """
    Simple EXIF-independent container for the image metadata we track
    """

    def __init__(self, **tags):
        self.tags = tags

    def __getattribute__(self, key):
        tags = object.__getattribute__(self, 'tags')
        if key in tags:
            return tags[key]
        else:
            return object.__getattribute__(self, key)

    def __getitem__(self, key):
        return self.tags[key]

    def __repr__(self):
        return '\n'.join('{0}: {1}'.format(*v) for v in self.tags.items())


def _get_metadata_field(tag):
    @property
    def getter(self):
        return self.metadata[tag]
    return getter


class BasePhoto(object):

    def __init__(self, path, metadata, is_valid=True):
        self.path = path
        self.file_ext = util.get_file_extension(self.path)
        self.metadata_type = util.get_type_from_ext(self.file_ext)
        self.metadata = metadata
        self.is_valid = is_valid

    width = _get_metadata_field('width')
    height = _get_metadata_field('height')
    make = _get_metadata_field('make')
    model = _get_metadata_field('model')
    focal_length = _get_metadata_field('focal_length')
    iso = _get_metadata_field('iso')
    shutter_speed = _get_metadata_field('shutter_speed')
    aperture = _get_metadata_field('aperture')
    timestamp = _get_metadata_field('timestamp')


class Photo(BasePhoto):
    """
    An image file (or movie) with embedded EXIF metadata
    """
    def __init__(self, path):
        try:
            metadata = read_exif(path)
            is_valid = True
            self.metadata_exc = None
        except ExifReadError as e:
            metadata = None
            is_valid = False
            self.metadata_exc = e

        BasePhoto.__init__(self, path, metadata, is_valid=is_valid)

    @property
    def checksum(self):
        return util.get_checksum(self.path)

    @property
    def size(self):
        return os.stat(self.path).st_size

    def edit_metadata(self, create_date=None, make=None, model=None,
                      new_path=None):
        """
        Modify EXIF metadata in place or create copy
        """
        raise NotImplementedError


class MockPhoto(BasePhoto):
    """
    For unit testing
    """
    def __init__(self, path, metadata, checksum, size):
        self._checksum = checksum
        self._size = size
        BasePhoto.__init__(self, path, metadata)

    @property
    def checksum(self):
        return self._checksum

    @property
    def size(self):
        return self._size


# ----------------------------------------------------------------------
# EXIF metadata processing


class ExifReadError(Exception):
    """Raised when EXIF metadata cannot be read from a file."""
    pass


def read_exif(path):
    """
    Read EXIF metadata from an image file.

    Tries ExifRead (pure Python) first, falls back to exiftool if available.
    """
    try:
        return _read_exif_exifread(path)
    except Exception as e:
        logger.debug(f"ExifRead failed for {path}: {e}")
        if _EXIFTOOL_AVAILABLE:
            logger.debug(f"Falling back to exiftool for {path}")
            return _read_exif_exiftool(path)
        else:
            raise ExifReadError(f"Failed to read EXIF from {path}: {e}") from e


def _read_exif_exifread(path):
    """Read EXIF using the exifread library (pure Python)."""
    with open(path, 'rb') as f:
        tags = exifread.process_file(f, details=False)

    if not tags:
        raise ExifReadError(f"No EXIF data found in {path}")

    width, height = _exifread_get_dimensions(tags)

    clean_tags = {
        'width': width,
        'height': height,
        'make': _exifread_get_string(tags, ['Image Make'], 'unknown'),
        'model': _exifread_get_string(tags, ['Image Model'], 'unknown'),
        'focal_length': _exifread_get_string(
            tags, ['EXIF FocalLength'], 'unknown'
        ),
        'timestamp': _exifread_get_timestamp(tags),
        'iso': _exifread_get_int(tags, ['EXIF ISOSpeedRatings'], -1),
        'shutter_speed': _exifread_get_string(
            tags, ['EXIF ShutterSpeedValue', 'EXIF ExposureTime'], 'unknown'
        ),
        'aperture': _exifread_get_float(tags, ['EXIF FNumber'], -1),
    }

    return ImageMetadata(**clean_tags)


def _exifread_get_string(tags, keys, default):
    """Get string value from first matching key."""
    for key in keys:
        if key in tags:
            return str(tags[key])
    return default


def _exifread_get_int(tags, keys, default):
    """Get integer value from first matching key."""
    for key in keys:
        if key in tags:
            try:
                val = tags[key].values[0]
                return int(val)
            except (IndexError, ValueError, TypeError):
                try:
                    return int(str(tags[key]))
                except ValueError:
                    pass
    return default


def _exifread_get_float(tags, keys, default):
    """Get float value from first matching key."""
    for key in keys:
        if key in tags:
            try:
                val = tags[key].values[0]
                if hasattr(val, 'num') and hasattr(val, 'den'):
                    # Handle Ratio type
                    return float(val.num) / float(val.den) if val.den else default
                return float(val)
            except (IndexError, ValueError, TypeError):
                try:
                    return float(str(tags[key]))
                except ValueError:
                    pass
    return default


def _exifread_get_dimensions(tags):
    """Extract image dimensions from ExifRead tags."""
    # Try RAW dimensions first
    width = _exifread_get_int(
        tags,
        ['EXIF ExifImageWidth', 'Image ImageWidth'],
        0
    )
    height = _exifread_get_int(
        tags,
        ['EXIF ExifImageLength', 'Image ImageLength'],
        0
    )

    if width == 0 or height == 0:
        raise ExifReadError("Could not determine image dimensions")

    return width, height


_EXIF_TIMESTAMP_FORMAT = '%Y:%m:%d %H:%M:%S'


def _exifread_get_timestamp(tags):
    """Extract timestamp from ExifRead tags."""
    timestamp_keys = [
        'EXIF DateTimeOriginal',
        'EXIF DateTimeDigitized',
        'Image DateTime',
    ]

    for key in timestamp_keys:
        if key in tags:
            timestamp_str = str(tags[key])
            try:
                return datetime.strptime(timestamp_str, _EXIF_TIMESTAMP_FORMAT)
            except ValueError:
                try:
                    return date_parser.parse(timestamp_str)
                except (ValueError, TypeError):
                    continue

    return None


# ----------------------------------------------------------------------
# exiftool fallback implementation


def _read_exif_exiftool(path):
    """Read EXIF using exiftool subprocess (fallback)."""
    try:
        exiftool_output = subprocess.check_output(
            ['exiftool', '-j', path],
            stderr=subprocess.DEVNULL
        )
    except subprocess.CalledProcessError as e:
        raise ExifReadError(f"exiftool failed for {path}") from e

    tags = json.loads(exiftool_output)[0]

    width, height = _exiftool_get_dimensions(tags)

    clean_tags = {
        'width': width,
        'height': height,
        'make': tags.get('Make', 'unknown'),
        'model': tags.get('Model', 'unknown'),
        'focal_length': tags.get('FocalLength', 'unknown'),
        'timestamp': _exiftool_get_timestamp(tags),
        'iso': tags.get('ISO', -1),
        'shutter_speed': tags.get(
            'ShutterSpeedValue', tags.get('ShutterSpeed', 'unknown')
        ),
        'aperture': tags.get('FNumber', -1),
    }

    return ImageMetadata(**clean_tags)


def _exiftool_get_dimensions(tags):
    """Extract image dimensions from exiftool tags."""
    if 'RawImageFullWidth' in tags:
        width = tags['RawImageFullWidth']
        height = tags['RawImageFullHeight']
    elif 'ImageWidth' in tags:
        width = tags['ImageWidth']
        height = tags['ImageHeight']
    else:
        raise ExifReadError("Could not determine image dimensions")

    return width, height


def _exiftool_get_timestamp(tags):
    """Extract timestamp from exiftool tags."""
    timestamp = tags.get('CreateDate') or tags.get('DateTimeOriginal')
    if timestamp is None:
        return None

    try:
        return datetime.strptime(timestamp, _EXIF_TIMESTAMP_FORMAT)
    except ValueError:
        try:
            return date_parser.parse(timestamp)
        except (ValueError, TypeError):
            return None
